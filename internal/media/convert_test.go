package media_test

import (
	"testing"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media"
)

func solid(w, h int, r, g, b byte) media.RGBAFrame {
	f := media.RGBAFrame{Width: w, Height: h, Data: make([]byte, w*h*4)}
	for i := 0; i < w*h; i++ {
		f.Data[i*4], f.Data[i*4+1], f.Data[i*4+2], f.Data[i*4+3] = r, g, b, 255
	}
	return f
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func TestRGBARoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		r, g, b byte
	}{
		{"black", 0, 0, 0},
		{"white", 255, 255, 255},
		{"red", 220, 30, 30},
		{"green", 30, 200, 30},
		{"blue", 30, 30, 220},
		{"gray", 128, 128, 128},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i420, err := media.RGBAToI420(solid(32, 18, tt.r, tt.g, tt.b))
			if err != nil {
				t.Fatalf("RGBAToI420: %v", err)
			}
			if !i420.Valid() || i420.Width != 32 || i420.Height != 18 {
				t.Fatalf("bad I420 frame %dx%d", i420.Width, i420.Height)
			}
			back, err := media.I420ToRGBA(i420)
			if err != nil {
				t.Fatalf("I420ToRGBA: %v", err)
			}
			for _, p := range []int{0, 5*32 + 7, 17*32 + 31} {
				got := [3]int{int(back.Data[p*4]), int(back.Data[p*4+1]), int(back.Data[p*4+2])}
				want := [3]int{int(tt.r), int(tt.g), int(tt.b)}
				for c := range got {
					if abs(got[c]-want[c]) > 4 {
						t.Errorf("pixel %d channel %d = %d, want %d (+-4)", p, c, got[c], want[c])
					}
				}
				if back.Data[p*4+3] != 255 {
					t.Errorf("alpha = %d, want 255", back.Data[p*4+3])
				}
			}
		})
	}
}

func TestRGBAToI420CropsOddSizes(t *testing.T) {
	got, err := media.RGBAToI420(solid(33, 19, 10, 20, 30))
	if err != nil {
		t.Fatalf("RGBAToI420: %v", err)
	}
	if got.Width != 32 || got.Height != 18 {
		t.Errorf("size = %dx%d, want 32x18", got.Width, got.Height)
	}
}

func TestInvalidFrames(t *testing.T) {
	tests := []struct {
		name string
		call func() error
		tag  string
		path string
	}{
		{"empty RGBA", func() error { _, err := media.RGBAToI420(media.RGBAFrame{}); return err }, "InvalidFrameSize", "RGBAToI420.InvalidFrameSize"},
		{"short RGBA buffer", func() error {
			_, err := media.RGBAToI420(media.RGBAFrame{Width: 4, Height: 4, Data: make([]byte, 8)})
			return err
		}, "InvalidFrameSize", "RGBAToI420.InvalidFrameSize"},
		{"invalid I420", func() error { _, err := media.I420ToRGBA(media.I420Frame{Width: 4, Height: 4}); return err }, "InvalidFrameSize", "I420ToRGBA.InvalidFrameSize"},
		{"odd NewI420", func() error { _, err := media.NewI420(3, 4); return err }, "InvalidFrameSize", "NewI420.InvalidFrameSize"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appErr, ok := errors.FromError(tt.call())
			if !ok {
				t.Fatal("expected *errors.Error")
			}
			if !appErr.HasRoute(tt.path) {
				t.Errorf("unexpected failure path: %s", appErr.Route())
			}
		})
	}
}

func BenchmarkRGBAToI420_720p(b *testing.B) {
	f := solid(1280, 720, 90, 120, 200)
	b.SetBytes(int64(len(f.Data)))
	for i := 0; i < b.N; i++ {
		if _, err := media.RGBAToI420(f); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkI420ToRGBA_720p(b *testing.B) {
	f, _ := media.RGBAToI420(solid(1280, 720, 90, 120, 200))
	b.SetBytes(int64(len(f.Data)))
	for i := 0; i < b.N; i++ {
		if _, err := media.I420ToRGBA(f); err != nil {
			b.Fatal(err)
		}
	}
}

func TestShrinkRGBA(t *testing.T) {
	tests := []struct {
		name         string
		w, h, max    int
		wantW, wantH int
	}{
		{"small frame is untouched", 640, 480, 1280, 640, 480},
		{"exactly at the limit", 1280, 720, 1280, 1280, 720},
		{"1080p", 1920, 1080, 1280, 960, 540},
		{"5MP", 2592, 1944, 1280, 864, 648},
		{"unlimited", 4000, 3000, 0, 4000, 3000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := solid(tt.w, tt.h, 10, 20, 30)
			got := media.ShrinkRGBA(src, tt.max)
			if got.Width != tt.wantW || got.Height != tt.wantH {
				t.Fatalf("size = %dx%d, want %dx%d", got.Width, got.Height, tt.wantW, tt.wantH)
			}
			if len(got.Data) != got.Width*got.Height*4 || got.Data[0] != 10 || got.Data[len(got.Data)-2] != 30 {
				t.Errorf("shrunk pixels are wrong")
			}
		})
	}
}
