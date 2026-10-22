package ui

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rm4n0s/errors"
	gui "github.com/rm4n0s/glutter/ui"

	"github.com/rm4n0s/desktop-mirror/internal/app"
	"github.com/rm4n0s/desktop-mirror/internal/camera"
	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/netutil"
	"github.com/rm4n0s/desktop-mirror/internal/rtc"
	"github.com/rm4n0s/desktop-mirror/internal/tlscert"
	"github.com/rm4n0s/desktop-mirror/internal/web"
)

// cameraPollInterval is how often the list of cameras is checked for plugged
// and unplugged ones.
const cameraPollInterval = 2 * time.Second

// Config holds the settings and codec factories the application is built from.
type Config struct {
	// ConfigDir stores the TLS certificate.
	ConfigDir string
	// Port is the preferred HTTPS port (a free one is used if taken).
	Port                   int
	UDPPortMin, UDPPortMax uint16
	STUNServers            []string

	EncoderFactory media.EncoderFactory
	DecoderFactory media.DecoderFactory
}

// controller connects the camera, the session and the home page. It implements
// host. Its methods run on any goroutine unless noted.
type controller struct {
	cfg     Config
	ctx     context.Context
	capture *camera.Capture
	session *app.Session

	// ifaces, session and initErr are set by newController and never change.
	ifaces  []netutil.Iface
	initErr error

	// updater is the page's update function; nil while there is no page.
	updater atomic.Pointer[updateFunc]

	// mu guards the camera selection, which the camera watcher, the page and
	// the capture goroutine all read.
	mu      sync.Mutex
	devices []camera.Device
	camID   string

	previewFrames, remoteFrames frameSink
	showPreview, showRemote     func(*viewModel, media.RGBAFrame)
}

// newController prepares the session and the camera. What cannot be prepared
// (no network, say) is kept in initErr and shown once the page is up.
func newController(ctx context.Context, cfg Config) *controller {
	c := &controller{cfg: cfg, ctx: ctx, capture: camera.New()}
	c.showPreview = c.frameSetter((*viewModel).setPreview)
	c.showRemote = c.frameSetter((*viewModel).setRemote)
	c.capture.OnFrame(c.onCameraFrame)
	c.capture.OnError(c.showError)
	c.initErr = c.initSession()
	return c
}

func (c *controller) initSession() error {
	ifaces, err := netutil.LANInterfaces()
	if err != nil {
		return err
	}
	c.ifaces = ifaces
	site, err := web.Site()
	if err != nil {
		return errors.NewErr("SiteUnavailable", err)
	}
	c.session, err = app.NewSession(app.Config{
		NewServer: app.NewServerFactory(c.certFor, site, c.cfg.Port),
		NewPeer: app.NewPeerFactory(rtc.Config{
			UDPPortMin:     c.cfg.UDPPortMin,
			UDPPortMax:     c.cfg.UDPPortMax,
			STUNServers:    c.cfg.STUNServers,
			EncoderFactory: c.cfg.EncoderFactory,
			DecoderFactory: c.cfg.DecoderFactory,
		}),
		OnStatus:      c.onStatus,
		OnRemoteFrame: c.onRemoteFrame,
		OnError:       c.showError,
	})
	return err
}

// update runs fn on the UI thread; false when there is no page to show it.
func (c *controller) update(fn func(*viewModel)) bool {
	u := c.updater.Load()
	if u == nil {
		return false
	}
	return (*u)(fn)
}

func (c *controller) attach(update updateFunc) {
	c.updater.Store(&update)
	// The page is still being built: start on another goroutine, so that the
	// first update arrives after it is shown.
	go c.start()
}

func (c *controller) detach() { c.updater.Store(nil) }

// close stops everything the controller started.
func (c *controller) close() {
	c.detach()
	if c.session != nil {
		c.session.Stop()
	}
	c.capture.Stop()
}

// start shows the network and the cameras, then waits for a phone.
func (c *controller) start() {
	if c.initErr != nil {
		c.showError(c.initErr)
		return
	}
	c.update(func(vm *viewModel) { vm.ifaces = c.ifaces })
	go camera.Watch(c.ctx, cameraPollInterval, c.onDevices)
	c.startSession(0)
}

// certFor returns a certificate that covers ip and every other LAN address, so
// switching interfaces does not invalidate what the phone already accepted.
func (c *controller) certFor(ip net.IP) (tls.Certificate, error) {
	ips := []net.IP{ip}
	for _, i := range c.ifaces {
		ips = append(ips, i.IP)
	}
	return tlscert.LoadOrCreate(c.ctx, c.cfg.ConfigDir, ips, time.Now())
}

// onDevices handles a new list of cameras: it keeps the camera in use while it
// is still there, and otherwise switches to the first one.
func (c *controller) onDevices(devices []camera.Device) {
	c.mu.Lock()
	c.devices = devices
	selected := -1
	for i, d := range devices {
		if d.ID == c.camID {
			selected = i
		}
	}
	if selected < 0 && len(devices) > 0 {
		selected = 0
	}
	id := ""
	if selected >= 0 {
		id = devices[selected].ID
	}
	changed := id != c.camID
	c.camID = id
	c.mu.Unlock()

	if selected < 0 {
		c.capture.Stop()
		c.previewFrames.reset()
		c.update(func(vm *viewModel) { vm.setCameras(nil, -1) })
		c.showError(errors.New("NoCameraFound", "No camera found: the phone's camera can still be viewed here"))
		return
	}
	c.update(func(vm *viewModel) { vm.setCameras(devices, selected) })
	if changed {
		c.startCamera(id)
	}
}

func (c *controller) selectCamera(i int) {
	c.mu.Lock()
	if i < 0 || i >= len(c.devices) {
		c.mu.Unlock()
		return
	}
	id := c.devices[i].ID
	c.camID = id
	c.mu.Unlock()
	// Switching waits for the old camera to be released, which the UI thread
	// should not.
	go c.startCamera(id)
}

func (c *controller) startCamera(id string) {
	if err := c.capture.Start(c.ctx, id); err != nil {
		c.showError(err)
		return
	}
	c.showError(nil)
}

func (c *controller) selectInterface(i int) { go c.startSession(i) }

func (c *controller) startSession(i int) {
	if c.session == nil || i < 0 || i >= len(c.ifaces) {
		return
	}
	if err := c.session.Start(c.ctx, c.ifaces[i].IP); err != nil {
		c.showError(err)
		return
	}
	c.showError(nil)
}

func (c *controller) newPairing() {
	if c.session == nil {
		return
	}
	if err := c.session.NewPairing(); err != nil {
		c.showError(err)
	}
}

func (c *controller) onStatus(st app.Status) {
	log.Printf("session: %s %s", st.State, st.URL)
	var qr *gui.Image
	if st.State == app.StateWaitingForPhone && st.URL != "" {
		var err error
		if qr, err = qrImage(c.ctx, st.URL); err != nil {
			c.showError(err)
		}
	}
	c.update(func(vm *viewModel) { vm.applyStatus(st, qr) })
}

// onCameraFrame runs on the capture goroutine for each captured frame.
func (c *controller) onCameraFrame(f media.RGBAFrame) {
	if c.session != nil && c.session.WantsFrames() {
		c.session.PushFrame(f)
	}
	c.previewFrames.submit(f, c.update, c.showPreview)
}

// onRemoteFrame runs on a pion goroutine; it converts off the UI thread and
// shows only the newest frame.
func (c *controller) onRemoteFrame(f media.I420Frame) {
	rgba, err := media.I420ToRGBA(f)
	if err != nil {
		c.logError(err)
		return
	}
	c.remoteFrames.submit(rgba, c.update, c.showRemote)
}

// frameSetter returns a function for the UI thread that makes an image of a
// frame and gives it to set.
func (c *controller) frameSetter(set func(*viewModel, *gui.Image)) func(*viewModel, media.RGBAFrame) {
	return func(vm *viewModel, f media.RGBAFrame) {
		img, err := imageFromRGBA(c.ctx, f)
		if err != nil {
			c.logError(err)
			return
		}
		set(vm, img)
	}
}

func (c *controller) showError(err error) {
	text := ""
	if err != nil {
		c.logError(err)
		text = err.Error()
	}
	c.update(func(vm *viewModel) { vm.err = text })
}

// logError writes the structured error (tag, metadata, stack) to stderr.
func (c *controller) logError(err error) {
	if appErr, ok := errors.FromError(err); ok {
		if j, jerr := appErr.ToJson(); jerr == nil {
			if b, merr := json.Marshal(j); merr == nil {
				log.Printf("error: %s", b)
				return
			}
		}
	}
	log.Printf("error: %v", err)
}
