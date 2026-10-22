package camera

import (
	"bytes"
	"image"
	"image/draw"
	"image/jpeg"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media"
)

// YUYVToRGBA converts a packed YUYV (YUY2) frame, with rows of stride bytes, to
// RGBA using the BT.601 full-range coefficients that webcams use.
func YUYVToRGBA(data []byte, width, height, stride int) (media.RGBAFrame, error) {
	if width <= 0 || height <= 0 || width%2 != 0 || stride < width*2 || len(data) < stride*(height-1)+width*2 {
		return media.RGBAFrame{}, errors.New("InvalidYUYVFrame", "YUYV frame does not fit its size",
			"width", width, "height", height, "stride", stride, "bytes", len(data))
	}
	dst := media.RGBAFrame{Width: width, Height: height, Data: make([]byte, width*height*4)}
	for y := 0; y < height; y++ {
		src := data[y*stride : y*stride+width*2]
		out := dst.Data[y*width*4 : (y+1)*width*4]
		for x := 0; x < width; x += 2 {
			y0, u, y1, v := int(src[2*x]), int(src[2*x+1])-128, int(src[2*x+2]), int(src[2*x+3])-128
			r := (359 * v) >> 8
			g := (88*u + 183*v) >> 8
			b := (454 * u) >> 8
			putRGBA(out[4*x:], y0+r, y0-g, y0+b)
			putRGBA(out[4*x+4:], y1+r, y1-g, y1+b)
		}
	}
	return dst, nil
}

func putRGBA(p []byte, r, g, b int) {
	p[0], p[1], p[2], p[3] = clamp(r), clamp(g), clamp(b), 255
}

func clamp(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// MJPEGToRGBA decodes one JPEG frame from a camera into RGBA.
func MJPEGToRGBA(data []byte) (media.RGBAFrame, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return media.RGBAFrame{}, errors.NewErr("MJPEGDecodeFailed", err)
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return media.RGBAFrame{}, errors.New("InvalidMJPEGFrame", "JPEG frame is empty")
	}
	dst := &image.RGBA{Pix: make([]byte, b.Dx()*b.Dy()*4), Stride: b.Dx() * 4, Rect: image.Rect(0, 0, b.Dx(), b.Dy())}
	draw.Draw(dst, dst.Rect, img, b.Min, draw.Src)
	return media.RGBAFrame{Width: b.Dx(), Height: b.Dy(), Data: dst.Pix}, nil
}
