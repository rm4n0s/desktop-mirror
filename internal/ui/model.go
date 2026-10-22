package ui

import (
	gui "github.com/rm4n0s/glutter/ui"

	"github.com/rm4n0s/desktop-mirror/internal/app"
	"github.com/rm4n0s/desktop-mirror/internal/camera"
	"github.com/rm4n0s/desktop-mirror/internal/netutil"
)

// viewModel is everything the home page shows. It is only touched on the UI
// thread, through the update function the page hands to its host.
type viewModel struct {
	cameras     []camera.Device
	cameraIndex int
	ifaces      []netutil.Iface
	ifaceIndex  int

	state  app.State
	status string
	// url is the pairing URL; link is the same while it is shown as text.
	url  string
	link string
	err  string

	preview            *gui.Image
	previewPlaceholder string
	qr                 *gui.Image
	qrPlaceholder      string
	remote             *gui.Image
}

const remotePlaceholder = "Scan the QR code with your phone\nto see its camera here"

func newViewModel() viewModel {
	return viewModel{
		status:             "Starting…",
		previewPlaceholder: "No camera",
		qrPlaceholder:      "Starting…",
	}
}

// swap makes next the image in *slot and disposes the one it replaces.
func swap(slot **gui.Image, next *gui.Image) {
	if *slot != nil {
		(*slot).Dispose()
	}
	*slot = next
}

func (vm *viewModel) setPreview(img *gui.Image) { swap(&vm.preview, img) }
func (vm *viewModel) setRemote(img *gui.Image)  { swap(&vm.remote, img) }
func (vm *viewModel) setQR(img *gui.Image)      { swap(&vm.qr, img) }

// setCameras shows the camera list with the camera at index selected (-1 when
// there is none).
func (vm *viewModel) setCameras(devices []camera.Device, selected int) {
	vm.cameras = devices
	vm.cameraIndex = selected
	if len(devices) == 0 {
		vm.setPreview(nil)
		vm.previewPlaceholder = "No camera found"
	}
}

// applyStatus reflects the session state. qr is the picture of st.URL, made by
// the caller off the UI thread; it is only used while waiting for the phone.
func (vm *viewModel) applyStatus(st app.Status, qr *gui.Image) {
	vm.state = st.State
	vm.url = st.URL
	vm.link = ""
	switch st.State {
	case app.StateWaitingForPhone:
		vm.status = "Waiting for the phone: scan the QR code"
		vm.setQR(qr)
		if qr != nil {
			vm.link = st.URL
		}
		vm.setRemote(nil)
	case app.StateNegotiating:
		vm.status = "Connecting to " + st.Phone + "…"
		vm.setQR(nil)
		vm.qrPlaceholder = "Connecting…"
	case app.StateConnected:
		vm.status = "Connected: " + st.Phone
		vm.setQR(nil)
		vm.qrPlaceholder = "Phone connected"
	case app.StateStopped:
		vm.status = "Stopped"
		vm.setQR(nil)
		vm.qrPlaceholder = ""
	}
}

// waiting is whether a QR code is up for a phone to scan: the link can then be
// copied and replaced.
func (vm *viewModel) waiting() bool { return vm.state == app.StateWaitingForPhone }

func (vm *viewModel) canHangUp() bool {
	return vm.state == app.StateNegotiating || vm.state == app.StateConnected
}
