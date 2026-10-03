package app_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/pion/webrtc/v4"

	"github.com/rm4n0s/desktop-mirror/internal/app"
	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/media/mediatest"
	"github.com/rm4n0s/desktop-mirror/internal/rtc"
	"github.com/rm4n0s/desktop-mirror/internal/signaling"
	"github.com/rm4n0s/desktop-mirror/internal/tlscert"
	"github.com/rm4n0s/desktop-mirror/internal/web"
)

// TestEndToEnd drives the production wiring (real HTTPS/WebSocket server, real
// pion peers over loopback) with a test phone that speaks the same signaling
// protocol as the web page. Only the codecs are stubs.
func TestEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const w, h = 64, 48
	loopback := net.IPv4(127, 0, 0, 1)

	cert, err := tlscert.LoadOrCreate(ctx, "", []net.IP{loopback}, time.Now())
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	site, err := web.Site()
	if err != nil {
		t.Fatalf("Site: %v", err)
	}

	desktopGot := make(chan media.I420Frame, 64)
	statuses := make(chan app.Status, 32)
	session, err := app.NewSession(app.Config{
		NewServer: app.NewServerFactory(func(net.IP) (tls.Certificate, error) { return cert, nil }, site, 0),
		NewPeer: app.NewPeerFactory(rtc.Config{
			IncludeLoopback: true,
			EncoderFactory:  mediatest.EncoderFactory,
			DecoderFactory:  mediatest.DecoderFactory,
		}),
		OnStatus:      func(st app.Status) { statuses <- st },
		OnRemoteFrame: func(f media.I420Frame) { desktopGot <- f },
		OnError:       func(err error) { t.Errorf("session error: %v", err) },
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Start(ctx, loopback); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer session.Stop()

	var pairURL string
	for st := range statuses {
		if st.State == app.StateWaitingForPhone {
			pairURL = st.URL
			break
		}
	}
	if !strings.HasPrefix(pairURL, "https://127.0.0.1:") {
		t.Fatalf("unexpected pairing URL %q", pairURL)
	}

	// The phone first loads the page, like a browser after scanning the QR code.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := client.Get(pairURL)
	if err != nil {
		t.Fatalf("GET pairing page: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pairing page status = %d", resp.StatusCode)
	}

	// Then it opens the WebSocket and sends hello.
	wsURL := "wss" + strings.TrimPrefix(pairURL, "https") + "ws"
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.CloseNow()

	phoneGot := make(chan media.I420Frame, 64)
	var phone *rtc.Peer
	phone, err = rtc.NewPeer(rtc.Config{
		IncludeLoopback: true,
		EncoderFactory:  mediatest.EncoderFactory,
		DecoderFactory:  mediatest.DecoderFactory,
		OnFrame:         func(f media.I420Frame) { phoneGot <- f },
		OnLocalCandidate: func(c webrtc.ICECandidateInit) {
			err := wsjson.Write(ctx, ws, signaling.Message{
				Type: signaling.TypeCandidate, Candidate: c.Candidate, SDPMid: c.SDPMid, SDPMLineIndex: c.SDPMLineIndex,
			})
			if err != nil && ctx.Err() == nil {
				t.Errorf("send candidate: %v", err)
			}
		},
		OnError: func(err error) { t.Errorf("phone peer error: %v", err) },
	})
	if err != nil {
		t.Fatalf("NewPeer(phone): %v", err)
	}
	defer phone.Close()

	if err := wsjson.Write(ctx, ws, signaling.Message{Type: signaling.TypeHello, UserAgent: "E2E phone"}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	// Answer the offer and keep relaying candidates, as app.js does.
	go func() {
		for {
			var m signaling.Message
			if err := wsjson.Read(ctx, ws, &m); err != nil {
				return
			}
			switch m.Type {
			case signaling.TypeOffer:
				answer, err := phone.AcceptOffer(ctx, m.SDP)
				if err != nil {
					t.Errorf("AcceptOffer: %v", err)
					return
				}
				if err := wsjson.Write(ctx, ws, signaling.Message{Type: signaling.TypeAnswer, SDP: answer}); err != nil {
					t.Errorf("send answer: %v", err)
					return
				}
			case signaling.TypeCandidate:
				if err := phone.AddRemoteCandidate(webrtc.ICECandidateInit{
					Candidate: m.Candidate, SDPMid: m.SDPMid, SDPMLineIndex: m.SDPMLineIndex,
				}); err != nil {
					t.Errorf("AddRemoteCandidate: %v", err)
				}
			}
		}
	}()

	// The phone's camera: a stream of identical pattern frames.
	phoneFrame := mediatest.Pattern(w, h, 0x42)
	go func() {
		tick := time.NewTicker(33 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := phone.WriteFrame(ctx, phoneFrame); err != nil {
					t.Errorf("phone WriteFrame: %v", err)
					return
				}
			}
		}
	}()

	// The desktop camera: RGBA frames pushed into the session.
	desktopRGBA := media.RGBAFrame{Width: w, Height: h, Data: bytes.Repeat([]byte{200, 100, 50, 255}, w*h)}
	wantOnPhone, err := media.RGBAToI420(desktopRGBA)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		tick := time.NewTicker(33 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				session.PushFrame(desktopRGBA)
			}
		}
	}()

	select {
	case f := <-desktopGot:
		if f.Width != w || f.Height != h || !bytes.Equal(f.Data, phoneFrame.Data) {
			t.Errorf("desktop received a wrong frame: %dx%d", f.Width, f.Height)
		}
	case <-ctx.Done():
		t.Fatal("desktop never received the phone's video")
	}
	select {
	case f := <-phoneGot:
		if f.Width != w || f.Height != h || !bytes.Equal(f.Data, wantOnPhone.Data) {
			t.Errorf("phone received a wrong frame: %dx%d", f.Width, f.Height)
		}
	case <-ctx.Done():
		t.Fatal("phone never received the desktop's video")
	}

	var connected bool
	for !connected {
		select {
		case st := <-statuses:
			connected = st.State == app.StateConnected && st.Phone == "E2E phone"
		case <-ctx.Done():
			t.Fatal("session never reported the connected phone")
		}
	}

	// Hanging up from the phone returns the session to waiting with a new URL.
	if err := wsjson.Write(ctx, ws, signaling.Message{Type: signaling.TypeBye}); err != nil {
		t.Fatalf("send bye: %v", err)
	}
	for {
		select {
		case st := <-statuses:
			if st.State == app.StateWaitingForPhone {
				if st.URL == pairURL {
					t.Error("pairing URL was not rotated after the phone left")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("session did not return to waiting after bye")
		}
	}
}
