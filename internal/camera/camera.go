// Package camera captures frames from the PC's cameras through Video4Linux2,
// with no cgo and no dependency beyond golang.org/x/sys. It is Linux only, like
// the UI toolkit it feeds.
package camera

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unsafe"

	"github.com/rm4n0s/errors"
	"golang.org/x/sys/unix"
)

// Device is a camera the user can pick.
type Device struct {
	// ID is the device node, e.g. /dev/video0.
	ID          string
	Description string
}

// devicePattern matches the nodes a camera can show up as.
const devicePattern = "/dev/video*"

// Devices lists the available video capture devices, in device order. Nodes
// that cannot capture video (a camera's metadata node, output devices) are
// left out, and so are nodes that cannot be opened at all.
func Devices(ctx context.Context) ([]Device, error) {
	paths, err := filepath.Glob(devicePattern)
	if err != nil {
		return nil, errors.NewErr("ListCamerasFailed", err)
	}
	sort.Slice(paths, func(i, j int) bool { return deviceLess(paths[i], paths[j]) })

	var devices []Device
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, errors.NewErr("ListCamerasCancelled", err)
		}
		if d, ok := probe(path); ok {
			devices = append(devices, d)
		}
	}
	return devices, nil
}

// deviceLess orders video10 after video2.
func deviceLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// probe reports the capture device at path, if it is one.
func probe(path string) (Device, bool) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return Device{}, false
	}
	defer unix.Close(fd) //nolint:errcheck // nothing to do when closing a probe fails

	var caps v4l2Capability
	if ioctl(fd, vidiocQueryCap, unsafe.Pointer(&caps)) != nil {
		return Device{}, false
	}
	flags := caps.Capabilities
	if flags&capDeviceCaps != 0 {
		flags = caps.DeviceCaps
	}
	if flags&capVideoCapture == 0 || flags&capStreaming == 0 {
		return Device{}, false
	}
	name := strings.TrimSpace(cString(caps.Card[:]))
	if name == "" {
		name = filepath.Base(path)
	}
	return Device{ID: path, Description: name}, true
}

// Watch calls onChange with the device list now and each time it changes, until
// ctx ends. Cameras can be plugged in or removed at any time, and the kernel
// offers no portable notification for it, so the list is polled. It blocks;
// onChange runs on the calling goroutine.
func Watch(ctx context.Context, interval time.Duration, onChange func([]Device)) {
	var last []Device
	first := true
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		devices, err := Devices(ctx)
		if err == nil && (first || !slices.Equal(devices, last)) {
			first = false
			last = devices
			onChange(devices)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// TargetWidth and TargetHeight are the capture size the camera is asked for:
// large enough to look good, small enough to encode in real time.
const (
	TargetWidth  = 1280
	TargetHeight = 720
	targetFPS    = 30
)

// FormatInfo describes one camera mode.
type FormatInfo struct {
	Width, Height int
	MaxFPS        float64
}

// BestFormat returns the index of the mode closest to 1280x720 that can reach
// about 30 fps, or -1 when there are no modes. Modes that are too slow only win
// when nothing faster exists. Of equally good modes the first wins.
func BestFormat(formats []FormatInfo) int {
	best, bestScore := -1, 0
	for i, f := range formats {
		if f.Width <= 0 || f.Height <= 0 {
			continue
		}
		score := abs(f.Width*f.Height - TargetWidth*TargetHeight)
		if f.MaxFPS < targetFPS-1 {
			score += 1 << 40 // prefer any fast-enough mode over a slow one
		}
		if best < 0 || score < bestScore {
			best, bestScore = i, score
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
