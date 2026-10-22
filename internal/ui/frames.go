package ui

import (
	"context"
	"image"
	"sync/atomic"

	"github.com/rm4n0s/errors"
	"github.com/skip2/go-qrcode"

	gui "github.com/rm4n0s/glutter/ui"

	"github.com/rm4n0s/desktop-mirror/internal/media"
)

// frameSink hands video frames from any goroutine to the UI thread. Frames
// arrive faster than they are painted, so it keeps only the newest one and
// queues at most one UI update at a time.
type frameSink struct {
	latest atomic.Pointer[media.RGBAFrame]
	queued atomic.Bool
}

// submit offers f. update runs a function on the UI thread (it reports false
// when there is no page to run it); show runs there with the newest frame.
func (s *frameSink) submit(f media.RGBAFrame, update func(func(*viewModel)) bool, show func(*viewModel, media.RGBAFrame)) {
	s.latest.Store(&f)
	if !s.queued.CompareAndSwap(false, true) {
		return
	}
	queued := update(func(vm *viewModel) {
		s.queued.Store(false)
		if f := s.latest.Swap(nil); f != nil {
			show(vm, *f)
		}
	})
	if !queued {
		s.queued.Store(false)
	}
}

// reset forgets a frame that was submitted but not shown yet.
func (s *frameSink) reset() { s.latest.Store(nil) }

// imageFromRGBA copies a Go-owned RGBA frame into an image. The frames are
// opaque, so they are their own premultiplied form.
func imageFromRGBA(ctx context.Context, f media.RGBAFrame) (*gui.Image, error) {
	img, err := gui.DecodeImageFromPixels(ctx, f.Data, f.Width, f.Height, gui.PixelFormatRGBA8888)
	if err != nil {
		return nil, errors.NewErr("FrameImageFailed", err).SetMetadata("width", f.Width).SetMetadata("height", f.Height)
	}
	return img, nil
}

// qrPixels is about how many pixels wide the QR code is made; each module gets
// a whole number of them, which keeps the edges sharp.
const qrPixels = 640

// qrImage draws url as a QR code.
func qrImage(ctx context.Context, url string) (*gui.Image, error) {
	code, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		return nil, errors.NewErr("QRGenerationFailed", err)
	}
	scale := max(1, qrPixels/len(code.Bitmap()))
	pic := code.Image(-scale)
	b := pic.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			rgba.Set(x, y, pic.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return imageFromRGBA(ctx, media.RGBAFrame{Width: b.Dx(), Height: b.Dy(), Data: rgba.Pix})
}
