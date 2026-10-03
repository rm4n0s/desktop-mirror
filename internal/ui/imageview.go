// Package ui is the Qt (miqt) front end. All code here runs on the Qt main
// thread unless noted.
package ui

import (
	"runtime"

	qt "github.com/mappu/miqt/qt6"

	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/qtutil"
)

// ImageView paints one image, centered and scaled to fit, on a dark
// background, or a placeholder text while there is no image.
type ImageView struct {
	*qt.QWidget
	img         *qt.QImage
	placeholder string
	smooth      bool
}

// NewImageView creates a view. smooth selects bilinear scaling (video);
// otherwise pixels stay sharp (QR codes).
func NewImageView(smooth bool) *ImageView {
	v := &ImageView{QWidget: qt.NewQWidget2(), smooth: smooth}
	v.SetSizePolicy2(qt.QSizePolicy__Expanding, qt.QSizePolicy__Expanding)
	v.OnPaintEvent(func(_ func(*qt.QPaintEvent), _ *qt.QPaintEvent) { v.paint() })
	return v
}

// SetImage shows img and takes ownership of it. nil clears the view.
func (v *ImageView) SetImage(img *qt.QImage) {
	qtutil.Release(v.img)
	v.img = img
	v.Update()
}

// SetPlaceholder sets the text shown while there is no image.
func (v *ImageView) SetPlaceholder(text string) {
	v.placeholder = text
	v.Update()
}

func (v *ImageView) paint() {
	p := qt.NewQPainter2(v.QPaintDevice)
	defer p.Delete()
	p.FillRect8(v.Rect(), qt.Black)

	if v.img == nil || v.img.IsNull() {
		if v.placeholder != "" {
			grey := qt.NewQColor3(170, 175, 185)
			defer grey.Delete()
			p.SetPen(grey)
			p.DrawText6(v.Rect(), int(qt.AlignCenter), v.placeholder)
		}
		p.End()
		return
	}
	if v.smooth {
		p.SetRenderHint(qt.QPainter__SmoothPixmapTransform)
	}
	w, h := v.Width(), v.Height()
	iw, ih := v.img.Width(), v.img.Height()
	// Fit the image inside the widget, keeping its aspect ratio.
	tw, th := w, w*ih/iw
	if th > h {
		tw, th = h*iw/ih, h
	}
	target := qt.NewQRect4((w-tw)/2, (h-th)/2, tw, th)
	defer target.Delete()
	p.DrawImage6(target, v.img)
	p.End()
}

// imageFromRGBA copies a Go-owned RGBA frame into a new QImage.
func imageFromRGBA(f media.RGBAFrame) *qt.QImage {
	view := qt.NewQImage6(&f.Data[0], f.Width, f.Height, int64(f.Width*4), qt.QImage__Format_RGBA8888)
	img := view.Copy() // detach from the Go buffer
	view.Delete()
	runtime.KeepAlive(f.Data)
	return img
}
