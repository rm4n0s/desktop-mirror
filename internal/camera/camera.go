// Package camera captures frames from the PC's cameras through Qt Multimedia.
// All methods must be called on the Qt main thread.
package camera

import (
	"bytes"
	"unsafe"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/multimedia"
	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/qtutil"
)

// Device is a camera the user can pick.
type Device struct {
	ID          []byte
	Description string
}

// Devices lists the available video inputs.
func Devices() []Device {
	var devices []Device
	qtutil.WithGCPaused(func() {
		inputs := multimedia.QMediaDevices_VideoInputs()
		devices = make([]Device, 0, len(inputs))
		for i := range inputs {
			devices = append(devices, Device{ID: inputs[i].Id(), Description: inputs[i].Description()})
		}
	})
	return devices
}

// Capture runs one camera at a time and delivers its frames.
type Capture struct {
	session *multimedia.QMediaCaptureSession
	sink    *multimedia.QVideoSink
	cam     *multimedia.QCamera

	onFrame func(*qt.QImage)
	onError func(error)
}

// New creates an idle Capture.
func New() *Capture {
	c := &Capture{
		session: multimedia.NewQMediaCaptureSession(),
		sink:    multimedia.NewQVideoSink(),
	}
	c.session.SetVideoSink(c.sink)
	c.sink.OnVideoFrameChanged(c.handleFrame)
	return c
}

// OnFrame sets the frame handler. The handler owns the *QImage (RGBA8888)
// and must free it with qtutil.Release.
func (c *Capture) OnFrame(fn func(*qt.QImage)) { c.onFrame = fn }

// OnError sets the handler for camera failures that happen after Start.
func (c *Capture) OnError(fn func(error)) { c.onError = fn }

// Start switches to the camera with the given id, stopping the current one.
func (c *Capture) Start(id []byte) error {
	c.Stop()
	var cam *multimedia.QCamera
	qtutil.WithGCPaused(func() {
		inputs := multimedia.QMediaDevices_VideoInputs()
		for i := range inputs {
			if bytes.Equal(inputs[i].Id(), id) {
				cam = multimedia.NewQCamera2(&inputs[i]) // QCamera keeps its own copy
				formats := inputs[i].VideoFormats()
				infos := make([]FormatInfo, len(formats))
				for j := range formats {
					size := formats[j].Resolution()
					infos[j] = FormatInfo{Width: size.Width(), Height: size.Height(), MaxFPS: float64(formats[j].MaxFrameRate())}
				}
				if best := BestFormat(infos); best >= 0 {
					cam.SetCameraFormat(&formats[best])
				}
				return
			}
		}
	})
	if cam == nil {
		return errors.New("CameraNotFound", "the selected camera is no longer available", "id", string(id))
	}
	cam.OnErrorOccurred(func(_ multimedia.QCamera__Error, msg string) {
		if c.onError != nil {
			c.onError(errors.New("CameraFailed", msg))
		}
	})
	c.cam = cam
	c.session.SetCamera(cam)
	cam.Start()
	return nil
}

// Stop stops the current camera, if any.
func (c *Capture) Stop() {
	if c.cam == nil {
		return
	}
	c.cam.Stop()
	c.session.SetCamera(nil)
	c.cam.Delete()
	c.cam = nil
}

func (c *Capture) handleFrame(frame *multimedia.QVideoFrame) {
	if c.onFrame == nil || !frame.IsValid() {
		return
	}
	img := frame.ToImage()
	if img == nil {
		return
	}
	if img.IsNull() {
		qtutil.Release(img)
		return
	}
	rgba := img.ConvertToFormat(qt.QImage__Format_RGBA8888)
	qtutil.Release(img)
	if rgba == nil || rgba.IsNull() {
		return
	}
	c.onFrame(rgba)
}

// ToRGBA copies a QImage in RGBA8888 format into a Go-owned frame.
func ToRGBA(img *qt.QImage) (media.RGBAFrame, error) {
	if img.Format() != qt.QImage__Format_RGBA8888 {
		return media.RGBAFrame{}, errors.New("InvalidFrameFormat", "expected an RGBA8888 image", "format", int(img.Format()))
	}
	w, h := img.Width(), img.Height()
	stride := int(img.BytesPerLine())
	if w <= 0 || h <= 0 || stride < w*4 {
		return media.RGBAFrame{}, errors.New("InvalidFrameSize", "image has invalid dimensions", "width", w, "height", h)
	}
	src := unsafe.Slice(img.ConstBits(), stride*h)
	dst := media.RGBAFrame{Width: w, Height: h, Data: make([]byte, w*h*4)}
	for row := 0; row < h; row++ {
		copy(dst.Data[row*w*4:(row+1)*w*4], src[row*stride:row*stride+w*4])
	}
	return dst, nil
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
// when nothing faster exists.
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
