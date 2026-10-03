package ui

import (
	"fmt"

	qt "github.com/mappu/miqt/qt6"
	"github.com/skip2/go-qrcode"

	"github.com/rm4n0s/desktop-mirror/internal/app"
	"github.com/rm4n0s/desktop-mirror/internal/camera"
	"github.com/rm4n0s/desktop-mirror/internal/netutil"
	"github.com/rm4n0s/errors"
)

const qrPixels = 640

// Window is the main window: pairing controls on the left, the phone's feed
// on the right.
type Window struct {
	win *qt.QMainWindow

	cameraBox *qt.QComboBox
	ifaceBox  *qt.QComboBox
	ifaceRow  *qt.QWidget
	preview   *ImageView
	qr        *ImageView
	urlLabel  *qt.QLabel
	status    *qt.QLabel
	errLabel  *qt.QLabel
	copyBtn   *qt.QPushButton
	newQRBtn  *qt.QPushButton
	hangupBtn *qt.QPushButton
	remote    *ImageView

	url string
}

func newWindow() *Window {
	w := &Window{win: qt.NewQMainWindow2()}
	w.win.SetWindowTitle("Desktop Mirror")
	w.win.Resize(1180, 720)

	left := qt.NewQVBoxLayout2()
	left.AddWidget(qt.NewQLabel3("PC camera").QWidget)
	w.cameraBox = qt.NewQComboBox2()
	left.AddWidget(w.cameraBox.QWidget)

	w.preview = NewImageView(true)
	w.preview.SetMinimumSize2(320, 180)
	w.preview.SetPlaceholder("No camera")
	left.AddWidget2(w.preview.QWidget, 1)

	ifaceLayout := qt.NewQVBoxLayout2()
	ifaceLayout.SetContentsMargins(0, 0, 0, 0)
	ifaceLayout.AddWidget(qt.NewQLabel3("Network").QWidget)
	w.ifaceBox = qt.NewQComboBox2()
	ifaceLayout.AddWidget(w.ifaceBox.QWidget)
	w.ifaceRow = qt.NewQWidget2()
	w.ifaceRow.SetLayout(ifaceLayout.QLayout)
	left.AddWidget(w.ifaceRow)

	w.qr = NewImageView(false)
	w.qr.SetMinimumSize2(280, 280)
	w.qr.SetPlaceholder("Starting…")
	left.AddWidget2(w.qr.QWidget, 2)

	w.urlLabel = qt.NewQLabel2()
	w.urlLabel.SetWordWrap(true)
	w.urlLabel.SetTextInteractionFlags(qt.TextSelectableByMouse)
	left.AddWidget(w.urlLabel.QWidget)

	buttons := qt.NewQHBoxLayout2()
	w.copyBtn = qt.NewQPushButton3("Copy link")
	w.newQRBtn = qt.NewQPushButton3("New QR code")
	w.hangupBtn = qt.NewQPushButton3("Disconnect")
	buttons.AddWidget(w.copyBtn.QWidget)
	buttons.AddWidget(w.newQRBtn.QWidget)
	buttons.AddWidget(w.hangupBtn.QWidget)
	left.AddLayout(buttons.QLayout)

	w.status = qt.NewQLabel3("Starting…")
	left.AddWidget(w.status.QWidget)
	w.errLabel = qt.NewQLabel2()
	w.errLabel.SetWordWrap(true)
	w.errLabel.SetStyleSheet("color: #c0392b;")
	left.AddWidget(w.errLabel.QWidget)

	leftPanel := qt.NewQWidget2()
	leftPanel.SetLayout(left.QLayout)
	leftPanel.SetFixedWidth(360)

	w.remote = NewImageView(true)
	w.remote.SetMinimumSize2(480, 270)
	w.remote.SetPlaceholder("Scan the QR code with your phone\nto see its camera here")

	root := qt.NewQHBoxLayout2()
	root.AddWidget(leftPanel)
	root.AddWidget2(w.remote.QWidget, 1)
	central := qt.NewQWidget2()
	central.SetLayout(root.QLayout)
	w.win.SetCentralWidget(central)

	w.copyBtn.SetEnabled(false)
	w.newQRBtn.SetEnabled(false)
	w.hangupBtn.SetEnabled(false)
	w.copyBtn.OnClicked(func() {
		if w.url != "" {
			qt.QGuiApplication_Clipboard().SetText(w.url)
		}
	})
	return w
}

// setCameras fills the camera selector, keeping the previous selection when
// that camera is still present, and returns the index now selected (-1 if none).
func (w *Window) setCameras(devices []camera.Device, keep []byte) int {
	blocked := w.cameraBox.BlockSignals(true)
	defer w.cameraBox.BlockSignals(blocked)
	w.cameraBox.Clear()
	selected := -1
	for i, d := range devices {
		w.cameraBox.AddItem(d.Description)
		if selected < 0 && keep != nil && string(d.ID) == string(keep) {
			selected = i
		}
	}
	if selected < 0 && len(devices) > 0 {
		selected = 0
	}
	if selected >= 0 {
		w.cameraBox.SetCurrentIndex(selected)
	}
	w.cameraBox.SetEnabled(len(devices) > 0)
	if len(devices) == 0 {
		w.preview.SetImage(nil)
		w.preview.SetPlaceholder("No camera found")
	}
	return selected
}

func (w *Window) setInterfaces(ifaces []netutil.Iface) {
	for _, i := range ifaces {
		w.ifaceBox.AddItem(i.String())
	}
	// Choosing is only worth showing when there is something to choose.
	w.ifaceRow.SetVisible(len(ifaces) > 1)
}

// applyStatus reflects the session state in the controls.
func (w *Window) applyStatus(st app.Status) {
	w.url = st.URL
	switch st.State {
	case app.StateWaitingForPhone:
		w.status.SetText("Waiting for the phone: scan the QR code")
		w.showQR(st.URL)
		w.remote.SetImage(nil)
	case app.StateNegotiating:
		w.status.SetText("Connecting to " + st.Phone + "…")
		w.qr.SetImage(nil)
		w.qr.SetPlaceholder("Connecting…")
		w.urlLabel.SetText("")
	case app.StateConnected:
		w.status.SetText("Connected: " + st.Phone)
		w.qr.SetImage(nil)
		w.qr.SetPlaceholder("Phone connected")
		w.urlLabel.SetText("")
	case app.StateStopped:
		w.status.SetText("Stopped")
		w.qr.SetImage(nil)
		w.qr.SetPlaceholder("")
		w.urlLabel.SetText("")
	}
	w.copyBtn.SetEnabled(st.State == app.StateWaitingForPhone)
	w.newQRBtn.SetEnabled(st.State == app.StateWaitingForPhone)
	w.hangupBtn.SetEnabled(st.State == app.StateNegotiating || st.State == app.StateConnected)
}

func (w *Window) showQR(url string) {
	if url == "" {
		w.qr.SetImage(nil)
		return
	}
	png, err := qrcode.Encode(url, qrcode.Medium, qrPixels)
	if err != nil {
		w.showError(errors.NewErr("QRGenerationFailed", err))
		return
	}
	w.qr.SetImage(qt.QImage_FromDataWithData(png))
	w.urlLabel.SetText(url)
}

func (w *Window) showError(err error) {
	if err == nil {
		w.errLabel.SetText("")
		return
	}
	w.errLabel.SetText(fmt.Sprint(err))
}
