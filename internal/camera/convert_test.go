package camera_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/camera"
)

func TestYUYVToRGBA(t *testing.T) {
	tests := []struct {
		name string
		yuyv []byte
		want []byte
	}{
		{"black", []byte{0, 128, 0, 128}, []byte{0, 0, 0, 255, 0, 0, 0, 255}},
		{"white", []byte{255, 128, 255, 128}, []byte{255, 255, 255, 255, 255, 255, 255, 255}},
		{"grey", []byte{128, 128, 128, 128}, []byte{128, 128, 128, 255, 128, 128, 128, 255}},
		{"red", []byte{76, 85, 76, 255}, []byte{254, 0, 0, 255, 254, 0, 0, 255}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := camera.YUYVToRGBA(tt.yuyv, 2, 1, 4)
			if err != nil {
				t.Fatal(err)
			}
			for i := range tt.want {
				if d := int(got.Data[i]) - int(tt.want[i]); d < -3 || d > 3 {
					t.Fatalf("pixels = %v, want about %v", got.Data, tt.want)
				}
			}
		})
	}
}

func TestYUYVToRGBAStride(t *testing.T) {
	// Two rows of one pixel pair, each row padded to 8 bytes.
	data := []byte{
		255, 128, 255, 128, 9, 9, 9, 9,
		0, 128, 0, 128, 9, 9, 9, 9,
	}
	got, err := camera.YUYVToRGBA(data, 2, 2, 8)
	if err != nil {
		t.Fatal(err)
	}
	if got.Data[0] != 255 || got.Data[8] != 0 {
		t.Errorf("rows were not read with the stride: %v", got.Data)
	}
}

func TestYUYVToRGBAInvalid(t *testing.T) {
	_, err := camera.YUYVToRGBA(make([]byte, 3), 2, 1, 4)
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("YUYVToRGBA.InvalidYUYVFrame") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

func TestMJPEGToRGBA(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 16, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 16; x++ {
			src.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	got, err := camera.MJPEGToRGBA(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 16 || got.Height != 8 || len(got.Data) != 16*8*4 {
		t.Fatalf("frame is %dx%d with %d bytes", got.Width, got.Height, len(got.Data))
	}
	if r, g, b, a := got.Data[0], got.Data[1], got.Data[2], got.Data[3]; r < 190 || g > 45 || b > 45 || a != 255 {
		t.Errorf("first pixel = %d,%d,%d,%d, want about 200,30,30,255", r, g, b, a)
	}
}

func TestMJPEGToRGBAInvalid(t *testing.T) {
	_, err := camera.MJPEGToRGBA([]byte("not a jpeg"))
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("MJPEGToRGBA.MJPEGDecodeFailed") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}
