//go:build darwin && cgo

package vtbox_test

import (
	"context"
	"math"
	"testing"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/media/h264"
	"github.com/rm4n0s/desktop-mirror/internal/media/vtbox"
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
			enc, err := vtbox.NewEncoder(tt.width, tt.height, 2_000_000)
			if err != nil {
				t.Fatalf("NewEncoder: %v", err)
			}
			defer func() { _ = enc.Close() }()
			dec, err := vtbox.NewDecoder()
			if err != nil {
				t.Fatalf("NewDecoder: %v", err)
			}
			defer func() { _ = dec.Close() }()

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
	enc, err := vtbox.NewEncoder(64, 48, 500_000)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	defer func() { _ = enc.Close() }()

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
	_, err := vtbox.NewEncoder(63, 48, 500_000)
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("NewEncoder.InvalidFrameSize") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

// nalTypes are the types of the NAL units of an access unit, in order.
func nalTypes(au []byte) []int {
	var types []int
	for _, nal := range h264.SplitAnnexB(au) {
		types = append(types, h264.Type(nal))
	}
	return types
}

func has(types []int, want int) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

func TestKeyframesCarryTheParameterSets(t *testing.T) {
	ctx := context.Background()
	enc, err := vtbox.NewEncoder(320, 240, 500_000)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	defer func() { _ = enc.Close() }()
	tests := []struct {
		name    string
		force   bool
		wantKey bool
	}{
		{"the first frame is a keyframe", false, true},
		{"the next one is not", false, false},
		{"nor the one after", false, false},
		{"a keyframe on request", true, true},
		{"and a plain frame again", false, false},
	}
	for n, tt := range tests {
		au, err := enc.Encode(ctx, gradient(t, 320, 240, n), tt.force)
		if err != nil {
			t.Fatalf("%s: Encode: %v", tt.name, err)
		}
		types := nalTypes(au)
		if len(types) == 0 {
			t.Fatalf("%s: the access unit has no NAL unit (%d bytes)", tt.name, len(au))
		}
		if got := h264.IsKeyframe(au); got != tt.wantKey {
			t.Errorf("%s: keyframe = %v, want %v (NAL units %v)", tt.name, got, tt.wantKey, types)
		}
		if tt.wantKey && (!has(types, h264.NALSPS) || !has(types, h264.NALPPS)) {
			t.Errorf("%s: the keyframe has no parameter sets: NAL units %v", tt.name, types)
		}
		if !tt.wantKey && !has(types, h264.NALSlice) {
			t.Errorf("%s: no slice: NAL units %v", tt.name, types)
		}
	}
}

func TestDecoderWaitsForAKeyframe(t *testing.T) {
	ctx := context.Background()
	enc, err := vtbox.NewEncoder(320, 240, 500_000)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	defer func() { _ = enc.Close() }()
	dec, err := vtbox.NewDecoder()
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer func() { _ = dec.Close() }()

	if _, ok, err := dec.Decode(ctx, nil); ok || err != nil {
		t.Errorf("an empty access unit gave ok=%v err=%v", ok, err)
	}
	key, err := enc.Encode(ctx, gradient(t, 320, 240, 0), true)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := enc.Encode(ctx, gradient(t, 320, 240, 1), false)
	if err != nil {
		t.Fatal(err)
	}
	// A stream joined in the middle: nothing comes out, and nothing fails, until the keyframe.
	if _, ok, err := dec.Decode(ctx, plain); ok || err != nil {
		t.Fatalf("a frame before the first keyframe gave ok=%v err=%v", ok, err)
	}
	if f, ok, err := dec.Decode(ctx, key); !ok || err != nil || f.Width != 320 || f.Height != 240 {
		t.Fatalf("the keyframe gave ok=%v err=%v size %dx%d", ok, err, f.Width, f.Height)
	}
	if _, ok, err := dec.Decode(ctx, plain); !ok || err != nil {
		t.Errorf("the frame after the keyframe gave ok=%v err=%v", ok, err)
	}
}

func TestDecoderTakesAStreamOfAnotherSize(t *testing.T) {
	ctx := context.Background()
	dec, err := vtbox.NewDecoder()
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer func() { _ = dec.Close() }()
	for _, size := range [][2]int{{320, 240}, {640, 360}, {240, 320}} {
		enc, err := vtbox.NewEncoder(size[0], size[1], 500_000)
		if err != nil {
			t.Fatalf("NewEncoder: %v", err)
		}
		au, err := enc.Encode(ctx, gradient(t, size[0], size[1], 0), true)
		_ = enc.Close()
		if err != nil {
			t.Fatal(err)
		}
		f, ok, err := dec.Decode(ctx, au)
		if err != nil || !ok {
			t.Fatalf("%dx%d: ok=%v err=%v", size[0], size[1], ok, err)
		}
		if f.Width != size[0] || f.Height != size[1] {
			t.Errorf("decoded %dx%d, want %dx%d", f.Width, f.Height, size[0], size[1])
		}
	}
}

func TestSetBitrate(t *testing.T) {
	ctx := context.Background()
	enc, err := vtbox.NewEncoder(320, 240, 500_000)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	defer func() { _ = enc.Close() }()
	if err := enc.SetBitrate(200_000); err != nil {
		t.Fatalf("SetBitrate: %v", err)
	}
	if au, err := enc.Encode(ctx, gradient(t, 320, 240, 0), false); err != nil || len(au) == 0 {
		t.Errorf("Encode after SetBitrate: %d bytes, %v", len(au), err)
	}
	err = enc.SetBitrate(0)
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("Encoder.SetBitrate.InvalidBitrate") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

func TestFailures(t *testing.T) {
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	tests := []struct {
		name  string
		run   func(t *testing.T) error
		route string
	}{
		{"a bitrate of zero", func(*testing.T) error {
			_, err := vtbox.NewEncoder(320, 240, 0)
			return err
		}, "NewEncoder.InvalidBitrate"},
		{"encoding after the context ended", func(t *testing.T) error {
			enc, err := vtbox.NewEncoder(320, 240, 500_000)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = enc.Close() }()
			_, err = enc.Encode(cancelled, gradient(t, 320, 240, 0), false)
			return err
		}, "Encoder.Encode.EncodeFailed"},
		{"encoding with a closed encoder", func(t *testing.T) error {
			enc, err := vtbox.NewEncoder(320, 240, 500_000)
			if err != nil {
				t.Fatal(err)
			}
			_ = enc.Close()
			_, err = enc.Encode(ctx, gradient(t, 320, 240, 0), false)
			return err
		}, "Encoder.Encode.EncodeFailed"},
		{"decoding after the context ended", func(t *testing.T) error {
			dec, err := vtbox.NewDecoder()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dec.Close() }()
			_, _, err = dec.Decode(cancelled, []byte{0, 0, 1, 0x41})
			return err
		}, "Decoder.Decode.DecodeFailed"},
		{"decoding with a closed decoder", func(t *testing.T) error {
			dec, err := vtbox.NewDecoder()
			if err != nil {
				t.Fatal(err)
			}
			_ = dec.Close()
			_, _, err = dec.Decode(ctx, []byte{0, 0, 1, 0x41})
			return err
		}, "Decoder.Decode.DecodeFailed"},
		{"parameter sets that are not parameter sets", func(t *testing.T) error {
			dec, err := vtbox.NewDecoder()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dec.Close() }()
			_, _, err = dec.Decode(ctx, h264.AppendAnnexB(nil, []byte{0x67, 1, 2, 3}, []byte{0x68, 4}, []byte{0x65, 5, 6, 7, 8}))
			return err
		}, "Decoder.Decode.DecodeFailed"},
		{"a slice that is garbage", func(t *testing.T) error {
			enc, err := vtbox.NewEncoder(320, 240, 500_000)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = enc.Close() }()
			dec, err := vtbox.NewDecoder()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dec.Close() }()
			key, err := enc.Encode(ctx, gradient(t, 320, 240, 0), true)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok, err := dec.Decode(ctx, key); !ok || err != nil {
				t.Fatalf("the keyframe gave ok=%v err=%v", ok, err)
			}
			_, _, err = dec.Decode(ctx, h264.AppendAnnexB(nil, []byte{0x65, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}))
			return err
		}, "Decoder.Decode.DecodeFailed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(t)
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T (%v)", err, err)
			}
			if !appErr.HasRoute(tt.route) {
				t.Errorf("unexpected failure path: %s", appErr.Route())
			}
		})
	}
}
