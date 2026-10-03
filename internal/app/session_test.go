package app_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/app"
	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/rtc"
	"github.com/rm4n0s/desktop-mirror/internal/signaling"
)

const wait = 5 * time.Second

type fakeServer struct {
	mu        sync.Mutex
	n         int
	onConnect func(ctx context.Context, c signaling.Conn)
}

func (f *fakeServer) Listen(context.Context) error    { return nil }
func (f *fakeServer) Serve(ctx context.Context) error { <-ctx.Done(); return nil }
func (f *fakeServer) URL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return "https://192.0.2.1:8443/s/token" + string(rune('0'+f.n)) + "/"
}
func (f *fakeServer) Rotate() (string, error) {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	return f.URL(), nil
}

type fakeConn struct {
	in   chan signaling.Message
	out  chan signaling.Message
	done chan struct{}
}

func newFakeConn() *fakeConn {
	return &fakeConn{in: make(chan signaling.Message, 16), out: make(chan signaling.Message, 16), done: make(chan struct{})}
}

func (c *fakeConn) UserAgent() string { return "FakePhone/1.0" }
func (c *fakeConn) Send(_ context.Context, m signaling.Message) error {
	c.out <- m
	return nil
}
func (c *fakeConn) Receive(ctx context.Context) (signaling.Message, error) {
	select {
	case m := <-c.in:
		return m, nil
	case <-ctx.Done():
		return signaling.Message{}, errors.NewErr("SignalingReceiveFailed", ctx.Err())
	}
}

type fakePeer struct {
	cb         app.PeerCallbacks
	answers    chan string
	candidates chan webrtc.ICECandidateInit
	frames     chan media.I420Frame
	closed     chan struct{}
}

func (p *fakePeer) CreateOffer(context.Context) (string, error) { return "offer-sdp", nil }
func (p *fakePeer) SetAnswer(_ context.Context, sdp string) error {
	p.answers <- sdp
	return nil
}
func (p *fakePeer) AddRemoteCandidate(c webrtc.ICECandidateInit) error {
	p.candidates <- c
	return nil
}
func (p *fakePeer) WriteFrame(_ context.Context, f media.I420Frame) error {
	p.frames <- f
	return nil
}
func (p *fakePeer) Close() error { close(p.closed); return nil }

type harness struct {
	t        *testing.T
	session  *app.Session
	server   *fakeServer
	peer     *fakePeer
	statuses chan app.Status
	errs     chan error
}

func newHarness(t *testing.T, helloTimeout time.Duration) *harness {
	t.Helper()
	h := &harness{
		t:        t,
		server:   &fakeServer{},
		statuses: make(chan app.Status, 32),
		errs:     make(chan error, 8),
		peer: &fakePeer{
			answers:    make(chan string, 4),
			candidates: make(chan webrtc.ICECandidateInit, 4),
			frames:     make(chan media.I420Frame, 4),
			closed:     make(chan struct{}),
		},
	}
	s, err := app.NewSession(app.Config{
		NewServer: func(_ net.IP, onConnect func(context.Context, signaling.Conn)) (app.SignalingServer, error) {
			h.server.onConnect = onConnect
			return h.server, nil
		},
		NewPeer: func(cb app.PeerCallbacks) (app.Peer, error) {
			h.peer.cb = cb
			return h.peer, nil
		},
		OnStatus:     func(st app.Status) { h.statuses <- st },
		OnError:      func(err error) { h.errs <- err },
		HelloTimeout: helloTimeout,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	h.session = s
	return h
}

func (h *harness) nextStatus(want app.State) app.Status {
	h.t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case st := <-h.statuses:
			if st.State == want {
				return st
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for state %v", want)
		}
	}
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(wait):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func (h *harness) connect(ctx context.Context) *fakeConn {
	c := newFakeConn()
	go h.server.onConnect(ctx, c)
	return c
}

func TestSessionPairsWithPhone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, time.Minute)

	if err := h.session.Start(ctx, net.IPv4(192, 0, 2, 1)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waiting := h.nextStatus(app.StateWaitingForPhone)
	if waiting.URL != "https://192.0.2.1:8443/s/token0/" {
		t.Fatalf("unexpected pairing URL %q", waiting.URL)
	}

	conn := h.connect(ctx)
	conn.in <- signaling.Message{Type: signaling.TypeHello, UserAgent: "Pixel 9"}

	if offer := recv(t, conn.out, "offer"); offer.Type != signaling.TypeOffer || offer.SDP != "offer-sdp" {
		t.Fatalf("expected the offer, got %+v", offer)
	}
	if st := h.nextStatus(app.StateNegotiating); st.Phone != "Pixel 9" {
		t.Errorf("phone = %q, want Pixel 9", st.Phone)
	}

	// Phone answers and trickles a candidate; the desktop trickles one back.
	mid := "0"
	conn.in <- signaling.Message{Type: signaling.TypeAnswer, SDP: "answer-sdp"}
	conn.in <- signaling.Message{Type: signaling.TypeCandidate, Candidate: "candidate:1 1 udp 1 192.0.2.9 5000 typ host", SDPMid: &mid}
	if got := recv(t, h.peer.answers, "answer"); got != "answer-sdp" {
		t.Errorf("answer = %q", got)
	}
	if got := recv(t, h.peer.candidates, "candidate"); got.Candidate == "" || *got.SDPMid != "0" {
		t.Errorf("candidate = %+v", got)
	}
	h.peer.cb.OnLocalCandidate(webrtc.ICECandidateInit{Candidate: "candidate:2 1 udp 1 192.0.2.1 6000 typ host", SDPMid: &mid})
	if got := recv(t, conn.out, "local candidate"); got.Type != signaling.TypeCandidate || got.Candidate == "" {
		t.Errorf("local candidate message = %+v", got)
	}

	h.peer.cb.OnState(rtc.StateConnected)
	if st := h.nextStatus(app.StateConnected); st.Phone != "Pixel 9" {
		t.Errorf("connected phone = %q", st.Phone)
	}

	// Camera frames reach the peer as I420.
	if !h.session.WantsFrames() {
		t.Error("WantsFrames = false while connected")
	}
	h.session.PushFrame(media.RGBAFrame{Width: 4, Height: 2, Data: make([]byte, 4*2*4)})
	if f := recv(t, h.peer.frames, "frame"); f.Width != 4 || f.Height != 2 || !f.Valid() {
		t.Errorf("frame = %dx%d valid=%v", f.Width, f.Height, f.Valid())
	}

	// bye ends the session, closes the peer and rotates the QR URL.
	conn.in <- signaling.Message{Type: signaling.TypeBye}
	recv(t, h.peer.closed, "peer close")
	st := h.nextStatus(app.StateWaitingForPhone)
	if st.URL != "https://192.0.2.1:8443/s/token1/" {
		t.Errorf("URL after disconnect = %q, want a rotated token", st.URL)
	}
	if h.session.WantsFrames() {
		t.Error("WantsFrames = true after disconnect")
	}
}

func TestSessionRejectsBadFirstMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, time.Minute)
	if err := h.session.Start(ctx, net.IPv4(192, 0, 2, 1)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.nextStatus(app.StateWaitingForPhone)

	conn := h.connect(ctx)
	conn.in <- signaling.Message{Type: signaling.TypeBye}

	err := recv(t, h.errs, "error")
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("Session.negotiate.UnexpectedMessage") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
	h.nextStatus(app.StateWaitingForPhone)
}

func TestSessionHelloTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, 50*time.Millisecond)
	if err := h.session.Start(ctx, net.IPv4(192, 0, 2, 1)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.nextStatus(app.StateWaitingForPhone)

	h.connect(ctx) // never sends hello

	err := recv(t, h.errs, "error")
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("SignalingReceiveFailed") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
	st := h.nextStatus(app.StateWaitingForPhone)
	if st.URL != "https://192.0.2.1:8443/s/token1/" {
		t.Errorf("URL = %q, want a rotated token", st.URL)
	}
}

func TestSessionNewPairingDisconnectsPhone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, time.Minute)
	if err := h.session.Start(ctx, net.IPv4(192, 0, 2, 1)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.nextStatus(app.StateWaitingForPhone)

	conn := h.connect(ctx)
	conn.in <- signaling.Message{Type: signaling.TypeHello}
	recv(t, conn.out, "offer")
	h.peer.cb.OnState(rtc.StateConnected)
	h.nextStatus(app.StateConnected)

	if err := h.session.NewPairing(); err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	recv(t, h.peer.closed, "peer close")
	st := h.nextStatus(app.StateWaitingForPhone)
	if st.URL != "https://192.0.2.1:8443/s/token1/" {
		t.Errorf("URL = %q, want a rotated token", st.URL)
	}
}

func TestSessionNewPairingWhenStopped(t *testing.T) {
	h := newHarness(t, time.Minute)
	err := h.session.NewPairing()
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("Session.NewPairing.SessionNotStarted") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

func TestSessionStopReportsStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, time.Minute)
	if err := h.session.Start(ctx, net.IPv4(192, 0, 2, 1)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.nextStatus(app.StateWaitingForPhone)
	h.session.Stop()
	h.nextStatus(app.StateStopped)
	if got := h.session.Status().State; got != app.StateStopped {
		t.Errorf("Status().State = %v, want stopped", got)
	}
}
