package ui

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net"
	"os"
	"sync/atomic"
	"time"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
	"github.com/mappu/miqt/qt6/multimedia"
	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/app"
	"github.com/rm4n0s/desktop-mirror/internal/camera"
	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/netutil"
	"github.com/rm4n0s/desktop-mirror/internal/rtc"
	"github.com/rm4n0s/desktop-mirror/internal/tlscert"
	"github.com/rm4n0s/desktop-mirror/internal/web"
)

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

// controller connects the camera, the session and the window.
type controller struct {
	cfg     Config
	ctx     context.Context
	win     *Window
	capture *camera.Capture
	session *app.Session

	devices []camera.Device
	ifaces  []netutil.Iface

	// remoteFrame holds the newest phone frame until the Qt thread paints it.
	remoteFrame atomic.Pointer[media.RGBAFrame]
	paintQueued atomic.Bool
}

// Run starts the Qt application and blocks until the window is closed.
func Run(cfg Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	qt.NewQApplication(os.Args)
	qt.QCoreApplication_SetApplicationName("desktop-mirror")

	c := &controller{cfg: cfg, ctx: ctx, win: newWindow(), capture: camera.New()}

	ifaces, err := netutil.LANInterfaces()
	if err != nil {
		qt.QMessageBox_Critical(c.win.win.QWidget, "Desktop Mirror", err.Error()+".\nConnect to a network and try again.")
		return err
	}
	c.ifaces = ifaces
	c.win.setInterfaces(ifaces)

	site, err := web.Site()
	if err != nil {
		return errors.NewErr("SiteUnavailable", err)
	}
	c.session, err = app.NewSession(app.Config{
		NewServer: app.NewServerFactory(c.certFor, site, cfg.Port),
		NewPeer: app.NewPeerFactory(rtc.Config{
			UDPPortMin:     cfg.UDPPortMin,
			UDPPortMax:     cfg.UDPPortMax,
			STUNServers:    cfg.STUNServers,
			EncoderFactory: cfg.EncoderFactory,
			DecoderFactory: cfg.DecoderFactory,
		}),
		OnStatus: func(st app.Status) {
			log.Printf("session: %s %s", st.State, st.URL)
			mainthread.Start(func() { c.win.applyStatus(st) })
		},
		OnRemoteFrame: c.onRemoteFrame,
		OnError:       func(err error) { mainthread.Start(func() { c.showError(err) }) },
	})
	if err != nil {
		return err
	}

	c.connectSignals()
	c.refreshCameras(nil)
	c.startSession(0)

	c.win.win.Show()
	qt.QApplication_Exec()

	cancel()
	c.session.Stop()
	c.capture.Stop()
	return nil
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

func (c *controller) connectSignals() {
	c.win.cameraBox.OnCurrentIndexChanged(func(i int) { c.startCamera(i) })
	c.win.ifaceBox.OnCurrentIndexChanged(func(i int) { c.startSession(i) })
	c.win.newQRBtn.OnClicked(func() { c.newPairing() })
	c.win.hangupBtn.OnClicked(func() { c.newPairing() })

	c.capture.OnFrame(c.onCameraFrame)
	c.capture.OnError(c.showError)

	// Cameras can be plugged in or removed at any time.
	devs := multimedia.NewQMediaDevices()
	devs.OnVideoInputsChanged(func() { c.refreshCameras(c.currentCameraID()) })
}

func (c *controller) currentCameraID() []byte {
	i := c.win.cameraBox.CurrentIndex()
	if i < 0 || i >= len(c.devices) {
		return nil
	}
	return c.devices[i].ID
}

func (c *controller) refreshCameras(keep []byte) {
	c.devices = camera.Devices()
	selected := c.win.setCameras(c.devices, keep)
	if selected < 0 {
		c.capture.Stop()
		c.showError(errors.New("NoCameraFound", "No camera found: the phone's camera can still be viewed here"))
		return
	}
	if keep == nil || string(c.devices[selected].ID) != string(keep) {
		c.startCamera(selected)
	}
}

func (c *controller) startCamera(i int) {
	if i < 0 || i >= len(c.devices) {
		return
	}
	if err := c.capture.Start(c.devices[i].ID); err != nil {
		c.showError(err)
		return
	}
	c.win.showError(nil)
}

func (c *controller) startSession(ifaceIndex int) {
	if ifaceIndex < 0 || ifaceIndex >= len(c.ifaces) {
		return
	}
	if err := c.session.Start(c.ctx, c.ifaces[ifaceIndex].IP); err != nil {
		c.showError(err)
		return
	}
	c.win.showError(nil)
}

func (c *controller) newPairing() {
	if err := c.session.NewPairing(); err != nil {
		c.showError(err)
	}
}

// onCameraFrame runs on the Qt thread for each captured frame.
func (c *controller) onCameraFrame(img *qt.QImage) {
	if c.session.WantsFrames() {
		if frame, err := camera.ToRGBA(img); err == nil {
			c.session.PushFrame(frame)
		}
	}
	c.win.preview.SetImage(img) // takes ownership
}

// onRemoteFrame runs on a pion goroutine; it converts off the Qt thread and
// queues at most one paint, always for the newest frame.
func (c *controller) onRemoteFrame(f media.I420Frame) {
	rgba, err := media.I420ToRGBA(f)
	if err != nil {
		c.logError(err)
		return
	}
	c.remoteFrame.Store(&rgba)
	if c.paintQueued.CompareAndSwap(false, true) {
		mainthread.Start(func() {
			c.paintQueued.Store(false)
			if f := c.remoteFrame.Swap(nil); f != nil {
				c.win.remote.SetImage(imageFromRGBA(*f))
			}
		})
	}
}

func (c *controller) showError(err error) {
	c.logError(err)
	c.win.showError(err)
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
