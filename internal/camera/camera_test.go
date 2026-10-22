package camera_test

import (
	"context"
	"testing"
	"time"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/camera"
	"github.com/rm4n0s/desktop-mirror/internal/media"
)

// TestCaptureDeliversFrames needs a real camera and skips without one.
func TestCaptureDeliversFrames(t *testing.T) {
	ctx := context.Background()
	devices, err := camera.Devices(ctx)
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devices) == 0 {
		t.Skip("no camera available")
	}
	t.Logf("using %q (%s)", devices[0].Description, devices[0].ID)

	capture := camera.New()
	frames := make(chan media.RGBAFrame, 16)
	capture.OnFrame(func(f media.RGBAFrame) {
		select {
		case frames <- f:
		default:
		}
	})
	failed := make(chan error, 1)
	capture.OnError(func(err error) { failed <- err })
	if err := capture.Start(ctx, devices[0].ID); err != nil {
		t.Skipf("camera could not be used: %v", err)
	}
	defer capture.Stop()

	timeout := time.After(8 * time.Second)
	var last media.RGBAFrame
	for n := 0; n < 10; n++ {
		select {
		case last = <-frames:
		case err := <-failed:
			t.Skipf("camera failed: %v", err)
		case <-timeout:
			t.Fatalf("received %d frames, want 10", n)
		}
	}
	if last.Width <= 0 || last.Height <= 0 || len(last.Data) != last.Width*last.Height*4 {
		t.Errorf("frame is %dx%d with %d bytes", last.Width, last.Height, len(last.Data))
	}
	t.Logf("%dx%d", last.Width, last.Height)
}

func TestStartUnknownCamera(t *testing.T) {
	err := camera.New().Start(context.Background(), "/dev/video-does-not-exist")
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T (%v)", err, err)
	}
	if !appErr.HasRoute("Capture.Start->openStream.CameraNotFound") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

func TestDevicesCancelled(t *testing.T) {
	devices, err := camera.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devices) == 0 {
		t.Skip("no camera available to cancel the listing of")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = camera.Devices(ctx)
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T (%v)", err, err)
	}
	if !appErr.HasRoute("Devices.ListCamerasCancelled") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}
