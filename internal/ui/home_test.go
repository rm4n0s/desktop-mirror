package ui

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/errors"
	"github.com/rm4n0s/glutter/foundation"
	"github.com/rm4n0s/glutter/gluttertest"
	m "github.com/rm4n0s/glutter/material"

	"github.com/rm4n0s/desktop-mirror/internal/app"
	"github.com/rm4n0s/desktop-mirror/internal/camera"
	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/netutil"
)

const testURL = "https://192.168.0.20:8443/s/abcdefghijklmnopqrstuv/"

// fakeHost records what the page asks of the application behind it.
type fakeHost struct {
	update updateFunc
	calls  []string
}

func (h *fakeHost) attach(update updateFunc) { h.update = update }
func (h *fakeHost) detach()                  { h.update = nil }
func (h *fakeHost) selectCamera(i int)       { h.calls = append(h.calls, "camera "+string(rune('0'+i))) }
func (h *fakeHost) selectInterface(i int)    { h.calls = append(h.calls, "iface "+string(rune('0'+i))) }
func (h *fakeHost) newPairing()              { h.calls = append(h.calls, "pairing") }

func newTester(t *testing.T) (*gluttertest.Tester, *fakeHost) {
	t.Helper()
	if !foundation.DebugMode {
		t.Skip("the platform override is a debug-only feature")
	}
	foundation.SetDebugDefaultTargetPlatformOverride(foundation.Some(foundation.TargetPlatformLinux))
	t.Cleanup(func() { foundation.SetDebugDefaultTargetPlatformOverride(foundation.None[foundation.TargetPlatform]()) })

	h := &fakeHost{}
	ts := gluttertest.New(t, gluttertest.Opts{Size: m.Size{Width: 1180, Height: 720}})
	ts.PumpWidget(newApplication(h))
	if h.update == nil {
		t.Fatal("the page did not attach to its host")
	}
	return ts, h
}

// show applies fn to the view model and lets the page rebuild.
func show(t *testing.T, ts *gluttertest.Tester, h *fakeHost, fn func(*viewModel)) {
	t.Helper()
	if !h.update(fn) {
		t.Fatal("the page refused the update")
	}
	ts.Pump(50 * time.Millisecond)
}

func buttonEnabled(t *testing.T, ts *gluttertest.Tester, label string) bool {
	t.Helper()
	f := gluttertest.Ancestor(gluttertest.ByText(label), gluttertest.ByType[*m.FilledButton]())
	return gluttertest.WidgetAs[*m.FilledButton](ts, f).Enabled()
}

func TestInitialStateHasNothingToClick(t *testing.T) {
	ts, _ := newTester(t)
	for _, label := range []string{"Copy link", "New QR code", "Disconnect"} {
		if buttonEnabled(t, ts, label) {
			t.Errorf("%q should be disabled before the session starts", label)
		}
	}
	if ts.Count(gluttertest.ByText("Starting…")) == 0 {
		t.Error("no Starting… text")
	}
}

func TestStatusTransitions(t *testing.T) {
	ts, h := newTester(t)

	qr, err := qrImage(context.Background(), testURL)
	if err != nil {
		t.Fatal(err)
	}
	if qr.Width() < 200 || qr.Width() != qr.Height() {
		t.Errorf("QR image is %dx%d, want a large square", qr.Width(), qr.Height())
	}

	t.Run("waiting shows the QR code and link", func(t *testing.T) {
		show(t, ts, h, func(vm *viewModel) {
			vm.applyStatus(app.Status{State: app.StateWaitingForPhone, URL: testURL}, qr)
		})
		if ts.Count(gluttertest.ByType[*m.RawImage]()) != 1 {
			t.Error("QR code was not drawn")
		}
		if ts.Count(gluttertest.ByText(testURL)) != 1 {
			t.Error("link not shown")
		}
		if !buttonEnabled(t, ts, "Copy link") || !buttonEnabled(t, ts, "New QR code") || buttonEnabled(t, ts, "Disconnect") {
			t.Error("waiting state: Copy link and New QR code should be enabled, Disconnect not")
		}
	})

	t.Run("new QR code asks the host", func(t *testing.T) {
		ts.Tap(gluttertest.ByText("New QR code"))
		ts.Pump(50 * time.Millisecond)
		if strings.Join(h.calls, ",") != "pairing" {
			t.Errorf("calls = %v", h.calls)
		}
	})

	t.Run("connecting hides the QR code", func(t *testing.T) {
		show(t, ts, h, func(vm *viewModel) {
			vm.applyStatus(app.Status{State: app.StateNegotiating, URL: testURL, Phone: "Pixel 9"}, nil)
		})
		if ts.Count(gluttertest.ByType[*m.RawImage]()) != 0 {
			t.Error("QR code should be cleared while connecting")
		}
		if ts.Count(gluttertest.ByText(testURL)) != 0 {
			t.Error("link should be hidden")
		}
		if ts.Count(gluttertest.ByText("Connecting to Pixel 9…")) != 1 {
			t.Error("status not shown")
		}
		if !buttonEnabled(t, ts, "Disconnect") || buttonEnabled(t, ts, "New QR code") || buttonEnabled(t, ts, "Copy link") {
			t.Error("connecting state: Disconnect should be enabled, the others not")
		}
	})

	t.Run("connected", func(t *testing.T) {
		show(t, ts, h, func(vm *viewModel) {
			vm.applyStatus(app.Status{State: app.StateConnected, URL: testURL, Phone: "Pixel 9"}, nil)
		})
		if ts.Count(gluttertest.ByText("Connected: Pixel 9")) != 1 || !buttonEnabled(t, ts, "Disconnect") {
			t.Error("connected state not shown")
		}
	})

	t.Run("back to waiting clears the old phone feed", func(t *testing.T) {
		frame := media.RGBAFrame{Width: 2, Height: 2, Data: make([]byte, 16)}
		img, err := imageFromRGBA(context.Background(), frame)
		if err != nil {
			t.Fatal(err)
		}
		show(t, ts, h, func(vm *viewModel) { vm.setRemote(img) })
		if ts.Count(gluttertest.ByType[*m.RawImage]()) != 1 {
			t.Fatal("phone feed not drawn")
		}
		show(t, ts, h, func(vm *viewModel) {
			vm.applyStatus(app.Status{State: app.StateWaitingForPhone, URL: testURL + "x"}, nil)
		})
		if ts.Count(gluttertest.ByType[*m.RawImage]()) != 0 {
			t.Error("remote view still shows the previous phone")
		}
		if !img.IsDisposed() {
			t.Error("the old phone frame was not released")
		}
	})
}

func TestInterfaceSelectorOnlyShowsWhenThereIsAChoice(t *testing.T) {
	ts, h := newTester(t)
	one := []netutil.Iface{{Name: "wlan0", IP: net.ParseIP("192.168.0.20")}}
	show(t, ts, h, func(vm *viewModel) { vm.ifaces = one })
	if ts.Count(gluttertest.ByText("Network")) != 0 {
		t.Error("selector visible with one interface")
	}
	show(t, ts, h, func(vm *viewModel) {
		vm.ifaces = append(one, netutil.Iface{Name: "eth0", IP: net.ParseIP("10.0.0.7")})
	})
	if ts.Count(gluttertest.ByText("Network")) != 1 {
		t.Error("selector hidden with two interfaces")
	}
}

func TestCameraList(t *testing.T) {
	ts, h := newTester(t)
	show(t, ts, h, func(vm *viewModel) { vm.setCameras(nil, -1) })
	if ts.Count(gluttertest.ByText("No camera found")) != 1 {
		t.Error("an empty camera list should say so")
	}

	devices := []camera.Device{{ID: "/dev/video0", Description: "Front"}, {ID: "/dev/video2", Description: "Back"}}
	show(t, ts, h, func(vm *viewModel) { vm.setCameras(devices, 1) })
	if ts.Count(gluttertest.ByText("Back")) == 0 {
		t.Error("the selected camera is not shown")
	}
}

func TestErrorsAreShownAndCleared(t *testing.T) {
	ts, h := newTester(t)
	show(t, ts, h, func(vm *viewModel) { vm.err = "boom" })
	if ts.Count(gluttertest.ByText("boom")) != 1 {
		t.Error("error not shown")
	}
	show(t, ts, h, func(vm *viewModel) { vm.err = "" })
	if ts.Count(gluttertest.ByText("boom")) != 0 {
		t.Error("error not cleared")
	}
}

func TestFrameSinkKeepsOnlyTheNewestFrame(t *testing.T) {
	var sink frameSink
	var queued []func(*viewModel)
	post := func(fn func(*viewModel)) bool { queued = append(queued, fn); return true }
	var shown []int
	show := func(_ *viewModel, f media.RGBAFrame) { shown = append(shown, f.Width) }

	for w := 1; w <= 3; w++ {
		sink.submit(media.RGBAFrame{Width: w, Height: 1}, post, show)
	}
	if len(queued) != 1 {
		t.Fatalf("%d updates queued, want 1", len(queued))
	}
	queued[0](&viewModel{})
	if len(shown) != 1 || shown[0] != 3 {
		t.Errorf("shown = %v, want only the newest frame", shown)
	}

	sink.submit(media.RGBAFrame{Width: 4, Height: 1}, post, show)
	if len(queued) != 2 {
		t.Errorf("a new frame after painting should queue an update again, have %d", len(queued))
	}
}

func TestFrameSinkWithoutPageDoesNotStick(t *testing.T) {
	var sink frameSink
	gone := func(func(*viewModel)) bool { return false }
	sink.submit(media.RGBAFrame{Width: 1, Height: 1}, gone, nil)

	var queued int
	sink.submit(media.RGBAFrame{Width: 2, Height: 1}, func(func(*viewModel)) bool { queued++; return true }, nil)
	if queued != 1 {
		t.Error("a sink must queue again once a page is back")
	}
}

func TestRGBAFramesKeepTheirPixels(t *testing.T) {
	f := media.RGBAFrame{Width: 2, Height: 1, Data: []byte{1, 2, 3, 255, 10, 20, 30, 255}}
	img, err := imageFromRGBA(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	f.Data[0] = 99 // the image must not alias the Go buffer
	if img.Width() != 2 || img.Height() != 1 {
		t.Fatalf("image is %dx%d", img.Width(), img.Height())
	}
	if got := img.Pixels(); got[0] != 1 || got[1] != 2 || got[2] != 3 || got[4] != 10 {
		t.Errorf("pixels = %v", got)
	}
}

func TestQRImageTooLong(t *testing.T) {
	_, err := qrImage(context.Background(), strings.Repeat("x", 4000))
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T (%v)", err, err)
	}
	if !appErr.HasRoute("qrImage.QRGenerationFailed") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}
