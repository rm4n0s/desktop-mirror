//go:build darwin && cgo

package rtc_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/media/vtbox"
	"github.com/rm4n0s/desktop-mirror/internal/rtc"
)

// picture is a smooth frame whose brightness depends on seed, so that the two directions can be told apart after a lossy codec.
func picture(t *testing.T, w, h int, seed byte) media.I420Frame {
	t.Helper()
	f, err := media.NewI420(w, h)
	if err != nil {
		t.Fatal(err)
	}
	y, u, v := f.Planes()
	for row := 0; row < h; row++ {
		for x := 0; x < w; x++ {
			y[row*w+x] = seed + byte((x+row)%64)
		}
	}
	for i := range u {
		u[i], v[i] = 128, 128
	}
	return f
}

func meanLuma(f media.I420Frame) float64 {
	y, _, _ := f.Planes()
	var sum float64
	for _, b := range y {
		sum += float64(b)
	}
	return sum / float64(len(y))
}

// TestPeersExchangeH264ThroughVideoToolbox connects two peers over loopback with the Mac's H.264 codec, as the program does with a
// phone: the frames go through VideoToolbox's encoder, pion's H.264 payloader and depacketizer, and VideoToolbox's decoder.
func TestPeersExchangeH264ThroughVideoToolbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const w, h = 320, 240
	desktopFrames := make(chan media.I420Frame, 64)
	phoneFrames := make(chan media.I420Frame, 64)

	var desktop, phone *rtc.Peer
	base := rtc.Config{
		IncludeLoopback: true,
		Codec:           media.CodecH264,
		EncoderFactory:  vtbox.NewEncoder,
		DecoderFactory:  vtbox.NewDecoder,
		OnError:         func(err error) { t.Errorf("peer error: %v", err) },
	}
	dc, pc := base, base
	dc.OnFrame = func(f media.I420Frame) {
		select {
		case desktopFrames <- f:
		default:
		}
	}
	pc.OnFrame = func(f media.I420Frame) {
		select {
		case phoneFrames <- f:
		default:
		}
	}
	dc.OnLocalCandidate = func(c webrtc.ICECandidateInit) {
		if err := phone.AddRemoteCandidate(c); err != nil {
			t.Errorf("phone.AddRemoteCandidate: %v", err)
		}
	}
	pc.OnLocalCandidate = func(c webrtc.ICECandidateInit) {
		if err := desktop.AddRemoteCandidate(c); err != nil {
			t.Errorf("desktop.AddRemoteCandidate: %v", err)
		}
	}

	var err error
	if desktop, err = rtc.NewPeer(dc); err != nil {
		t.Fatalf("NewPeer(desktop): %v", err)
	}
	defer func() { _ = desktop.Close() }()
	if phone, err = rtc.NewPeer(pc); err != nil {
		t.Fatalf("NewPeer(phone): %v", err)
	}
	defer func() { _ = phone.Close() }()

	offer, err := desktop.CreateOffer(ctx)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	answer, err := phone.AcceptOffer(ctx, offer)
	if err != nil {
		t.Fatalf("AcceptOffer: %v", err)
	}
	if err := desktop.SetAnswer(ctx, answer); err != nil {
		t.Fatalf("SetAnswer: %v", err)
	}

	send := func(p *rtc.Peer, f media.I420Frame) {
		tick := time.NewTicker(33 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := p.WriteFrame(ctx, f); err != nil {
					t.Errorf("WriteFrame: %v", err)
					return
				}
			}
		}
	}
	dark, bright := picture(t, w, h, 30), picture(t, w, h, 150)
	go send(desktop, dark)
	go send(phone, bright)

	tests := []struct {
		name   string
		frames <-chan media.I420Frame
		want   media.I420Frame
	}{
		{"phone to desktop", desktopFrames, bright},
		{"desktop to phone", phoneFrames, dark},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			select {
			case f := <-tt.frames:
				if f.Width != w || f.Height != h {
					t.Fatalf("received a frame of %dx%d, want %dx%d", f.Width, f.Height, w, h)
				}
				if got, want := meanLuma(f), meanLuma(tt.want); math.Abs(got-want) > 6 {
					t.Errorf("the mean brightness of the frame is %.1f, want about %.1f", got, want)
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for a frame")
			}
		})
	}
}
