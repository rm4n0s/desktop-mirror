package ui

import (
	"os"
	"strings"
	"testing"
	"unsafe"

	qt "github.com/mappu/miqt/qt6"

	"github.com/rm4n0s/desktop-mirror/internal/app"
	"github.com/rm4n0s/desktop-mirror/internal/media"
)

// Qt allows one QApplication per process and ties it to this goroutine's OS
// thread, so every UI check shares one test that runs on that goroutine.
func TestWindow(t *testing.T) {
	qt.NewQApplication(append(os.Args[:1:1], "-platform", "offscreen"))
	w := newWindow()

	const url = "https://192.168.0.20:8443/s/abcdefghijklmnopqrstuv/"

	t.Run("initial state has nothing to click", func(t *testing.T) {
		if w.newQRBtn.IsEnabled() || w.copyBtn.IsEnabled() || w.hangupBtn.IsEnabled() {
			t.Error("buttons should be disabled before the session starts")
		}
	})

	t.Run("waiting shows the QR code and link", func(t *testing.T) {
		w.applyStatus(app.Status{State: app.StateWaitingForPhone, URL: url})
		if w.qr.img == nil || w.qr.img.IsNull() {
			t.Fatal("QR code was not rendered")
		}
		if w.qr.img.Width() < 200 || w.qr.img.Width() != w.qr.img.Height() {
			t.Errorf("QR image is %dx%d, want a large square", w.qr.img.Width(), w.qr.img.Height())
		}
		if w.urlLabel.Text() != url {
			t.Errorf("link = %q", w.urlLabel.Text())
		}
		if !w.newQRBtn.IsEnabled() || !w.copyBtn.IsEnabled() || w.hangupBtn.IsEnabled() {
			t.Error("waiting state: New QR code and Copy should be enabled, Disconnect not")
		}
	})

	t.Run("connecting hides the QR code", func(t *testing.T) {
		w.applyStatus(app.Status{State: app.StateNegotiating, URL: url, Phone: "Pixel 9"})
		if w.qr.img != nil {
			t.Error("QR code should be cleared while connecting")
		}
		if w.urlLabel.Text() != "" {
			t.Errorf("link should be hidden, got %q", w.urlLabel.Text())
		}
		if !strings.Contains(w.status.Text(), "Pixel 9") {
			t.Errorf("status = %q", w.status.Text())
		}
		if !w.hangupBtn.IsEnabled() || w.newQRBtn.IsEnabled() {
			t.Error("connecting state: Disconnect should be enabled, New QR code not")
		}
	})

	t.Run("connected", func(t *testing.T) {
		w.applyStatus(app.Status{State: app.StateConnected, URL: url, Phone: "Pixel 9"})
		if !strings.Contains(w.status.Text(), "Connected") || !w.hangupBtn.IsEnabled() {
			t.Errorf("status = %q, hangup enabled = %v", w.status.Text(), w.hangupBtn.IsEnabled())
		}
	})

	t.Run("back to waiting clears the old phone feed", func(t *testing.T) {
		w.remote.SetImage(imageFromRGBA(media.RGBAFrame{Width: 2, Height: 2, Data: make([]byte, 16)}))
		w.applyStatus(app.Status{State: app.StateWaitingForPhone, URL: url + "x"})
		if w.remote.img != nil {
			t.Error("remote view still shows the previous phone")
		}
	})

	t.Run("interface selector only shows when there is a choice", func(t *testing.T) {
		w.setInterfaces(nil)
		if w.ifaceRow.IsVisibleTo(w.win.QWidget) {
			t.Error("selector visible without interfaces")
		}
	})

	t.Run("errors are shown and cleared", func(t *testing.T) {
		w.showError(os.ErrNotExist)
		if w.errLabel.Text() == "" {
			t.Error("error not shown")
		}
		w.showError(nil)
		if w.errLabel.Text() != "" {
			t.Error("error not cleared")
		}
	})

	t.Run("RGBA frames keep their pixels", func(t *testing.T) {
		f := media.RGBAFrame{Width: 2, Height: 1, Data: []byte{1, 2, 3, 255, 10, 20, 30, 255}}
		img := imageFromRGBA(f)
		defer func() { img.Delete() }()
		f.Data[0] = 99 // the QImage must not alias the Go buffer
		if img.Width() != 2 || img.Height() != 1 {
			t.Fatalf("image is %dx%d", img.Width(), img.Height())
		}
		got := unsafe.Slice(img.ConstScanLine(0), 8)
		if got[0] != 1 || got[1] != 2 || got[2] != 3 || got[4] != 10 {
			t.Errorf("pixels = %v", got)
		}
	})
}
