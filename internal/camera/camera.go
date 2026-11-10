// Package camera captures frames from the PC's cameras: through Video4Linux2
// on Linux, with no cgo and no dependency beyond golang.org/x/sys, and through
// AVFoundation on a Mac (cgo; a Mac build without cgo lists no camera).
package camera

import (
	"context"
	"slices"
	"time"
)

// Device is a camera the user can pick.
type Device struct {
	// ID is the device node on Linux, e.g. /dev/video0, and the unique ID of
	// the capture device on a Mac.
	ID          string
	Description string
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
