package rtc_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/media/mediatest"
	"github.com/rm4n0s/desktop-mirror/internal/rtc"
)

// TestPeersExchangeVideo connects two peers over loopback (one playing the
// phone) and checks that frames flow in both directions, intact.
func TestPeersExchangeVideo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const w, h = 64, 48
	desktopFrames := make(chan media.I420Frame, 64)
	phoneFrames := make(chan media.I420Frame, 64)

	var desktop, phone *rtc.Peer
	base := rtc.Config{
		IncludeLoopback: true,
		EncoderFactory:  mediatest.EncoderFactory,
		DecoderFactory:  mediatest.DecoderFactory,
		OnError:         func(err error) { t.Errorf("peer error: %v", err) },
	}
	dc, pc := base, base
	dc.OnFrame = func(f media.I420Frame) { desktopFrames <- f }
	pc.OnFrame = func(f media.I420Frame) { phoneFrames <- f }
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
	defer desktop.Close()
	if phone, err = rtc.NewPeer(pc); err != nil {
		t.Fatalf("NewPeer(phone): %v", err)
	}
	defer phone.Close()

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

	go pump(ctx, t, desktop, 0x10, w, h)
	go pump(ctx, t, phone, 0x80, w, h)

	tests := []struct {
		name   string
		frames <-chan media.I420Frame
		seed   byte
	}{
		{"phone to desktop", desktopFrames, 0x80},
		{"desktop to phone", phoneFrames, 0x10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			select {
			case f := <-tt.frames:
				want := mediatest.Pattern(w, h, tt.seed)
				if f.Width != w || f.Height != h || !bytes.Equal(f.Data, want.Data) {
					t.Fatalf("received frame differs: got %dx%d (%d bytes)", f.Width, f.Height, len(f.Data))
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for a frame")
			}
		})
	}
}

func pump(ctx context.Context, t *testing.T, p *rtc.Peer, seed byte, w, h int) {
	tick := time.NewTicker(33 * time.Millisecond)
	defer tick.Stop()
	f := mediatest.Pattern(w, h, seed)
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
