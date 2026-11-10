//go:build !(darwin && cgo)

package camera

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"unsafe"

	"github.com/rm4n0s/errors"
	"golang.org/x/sys/unix"
)

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
