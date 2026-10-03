package camera_test

import (
	"os"
	"testing"

	qt "github.com/mappu/miqt/qt6"

	"github.com/rm4n0s/desktop-mirror/internal/camera"
	"github.com/rm4n0s/desktop-mirror/internal/qtutil"
)

// TestCaptureDeliversFrames needs a real camera and skips without one. Qt
// objects are only touched from this goroutine, which NewQApplication pins to
// its OS thread.
func TestCaptureDeliversFrames(t *testing.T) {
	t.Setenv("QT_QPA_PLATFORM", "offscreen")
	qt.NewQApplication(append(os.Args[:1:1], "-platform", "offscreen"))

	devices := camera.Devices()
	if len(devices) == 0 {
		t.Skip("no camera available")
	}
	t.Logf("using %q", devices[0].Description)

	capture := camera.New()
	frames := 0
	var width, height int
	var toRGBAErr error
	capture.OnFrame(func(img *qt.QImage) {
		defer qtutil.Release(img)
		frames++
		if frames == 5 {
			width, height = img.Width(), img.Height()
			_, toRGBAErr = camera.ToRGBA(img)
		}
		if frames == 10 {
			qt.QCoreApplication_Quit()
		}
	})
	var captureErr error
	capture.OnError(func(err error) { captureErr = err; qt.QCoreApplication_Quit() })
	if err := capture.Start(devices[0].ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer capture.Stop()

	timeout := qt.NewQTimer()
	timeout.SetSingleShot(true)
	timeout.OnTimeout(func() { qt.QCoreApplication_Quit() })
	timeout.Start(8000)
	qt.QApplication_Exec()

	if captureErr != nil {
		t.Skipf("camera could not be used: %v", captureErr)
	}
	if frames < 10 {
		t.Fatalf("received %d frames, want >= 10", frames)
	}
	if width <= 0 || height <= 0 {
		t.Errorf("frame size %dx%d", width, height)
	}
	if toRGBAErr != nil {
		t.Errorf("ToRGBA: %v", toRGBAErr)
	}
	t.Logf("%d frames, %dx%d", frames, width, height)
}
