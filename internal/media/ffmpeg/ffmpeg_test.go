package ffmpeg_test

import (
	"context"
	"math"
	"testing"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/media/ffmpeg"
)

// gradient returns a smooth frame that shifts with n, so inter-frame
// prediction has something to do.
func gradient(t *testing.T, w, h, n int) media.I420Frame {
	t.Helper()
	f, err := media.NewI420(w, h)
	if err != nil {
		t.Fatal(err)
	}
	y, u, v := f.Planes()
	for row := 0; row < h; row++ {
		for x := 0; x < w; x++ {
			y[row*w+x] = byte(16 + (x+row+n*2)%200)
		}
	}
	for i := range u {
		u[i], v[i] = 100, 150
	}
	return f
}

func psnr(a, b []byte) float64 {
	var se float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		se += d * d
	}
	if se == 0 {
		return 99
	}
	return 10 * math.Log10(255*255/(se/float64(len(a))))
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
	}{
		{"vga", 640, 480},
		{"hd", 1280, 720},
		{"portrait", 480, 640},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			enc, err := ffmpeg.NewEncoder(tt.width, tt.height, 2_000_000)
			if appErr, ok := errors.FromError(err); ok && appErr.Tag == "EncoderUnavailable" {
				t.Skip("FFmpeg has no libvpx encoder")
			}
			if err != nil {
				t.Fatalf("NewEncoder: %v", err)
			}
			defer enc.Close()
			dec, err := ffmpeg.NewDecoder()
			if err != nil {
				t.Fatalf("NewDecoder: %v", err)
			}
			defer dec.Close()

			decoded := 0
			for n := 0; n < 10; n++ {
				src := gradient(t, tt.width, tt.height, n)
				au, err := enc.Encode(ctx, src, n == 0)
				if err != nil {
					t.Fatalf("Encode frame %d: %v", n, err)
				}
				if len(au) == 0 {
					continue
				}
				got, ok, err := dec.Decode(ctx, au)
				if err != nil {
					t.Fatalf("Decode frame %d: %v", n, err)
				}
				if !ok {
					continue
				}
				decoded++
				if got.Width != tt.width || got.Height != tt.height {
					t.Fatalf("decoded %dx%d, want %dx%d", got.Width, got.Height, tt.width, tt.height)
				}
				if p := psnr(src.Data, got.Data); p < 30 {
					t.Errorf("frame %d PSNR %.1f dB, want >= 30", n, p)
				}
			}
			if decoded < 8 {
				t.Errorf("decoded only %d of 10 frames", decoded)
			}
		})
	}
}

func TestEncodeRejectsWrongSize(t *testing.T) {
	enc, err := ffmpeg.NewEncoder(64, 48, 500_000)
	if appErr, ok := errors.FromError(err); ok && appErr.Tag == "EncoderUnavailable" {
		t.Skip("FFmpeg has no libvpx encoder")
	}
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	defer enc.Close()

	_, err = enc.Encode(context.Background(), gradient(t, 32, 32, 0), false)
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("Encoder.Encode.EncodeFailed") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

func TestNewEncoderRejectsOddSize(t *testing.T) {
	_, err := ffmpeg.NewEncoder(63, 48, 500_000)
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("NewEncoder.InvalidFrameSize") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}
