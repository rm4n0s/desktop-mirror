// Package app holds the pairing session: it owns the signaling server and,
// for each phone that connects, a WebRTC peer. It has no Qt dependency.
package app

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/rtc"
	"github.com/rm4n0s/desktop-mirror/internal/signaling"
)

// maxSendWidth caps the width of frames sent to the phone; wider camera
// frames are shrunk so software VP8 encoding keeps up.
const maxSendWidth = 1280

// State is the pairing session state.
type State int

const (
	StateStopped State = iota
	StateWaitingForPhone
	StateNegotiating
	StateConnected
)

func (s State) String() string {
	return [...]string{"stopped", "waiting for phone", "connecting", "connected"}[s]
}

// Status is published to the UI whenever it changes.
type Status struct {
	State State
	// URL is the pairing URL for the QR code (empty when stopped).
	URL string
	// Phone describes the connected phone (its user agent).
	Phone string
}

// SignalingServer is the part of signaling.Server the session uses.
type SignalingServer interface {
	Listen(ctx context.Context) error
	Serve(ctx context.Context) error
	URL() string
	Rotate() (string, error)
}

// Peer is the part of rtc.Peer the session uses.
type Peer interface {
	CreateOffer(ctx context.Context) (string, error)
	SetAnswer(ctx context.Context, sdp string) error
	AddRemoteCandidate(c webrtc.ICECandidateInit) error
	WriteFrame(ctx context.Context, f media.I420Frame) error
	Close() error
}

// ServerFactory builds a signaling server bound to ip that reports phones
// through onConnect.
type ServerFactory func(ip net.IP, onConnect func(ctx context.Context, c signaling.Conn)) (SignalingServer, error)

// PeerFactory builds a peer wired to the given callbacks.
type PeerFactory func(cb PeerCallbacks) (Peer, error)

// PeerCallbacks are the events a Peer reports.
type PeerCallbacks struct {
	OnLocalCandidate func(webrtc.ICECandidateInit)
	OnState          func(rtc.State)
	OnFrame          func(media.I420Frame)
	OnError          func(error)
}

// Config configures a Session. Callbacks may be called from any goroutine
// and must not block.
type Config struct {
	NewServer ServerFactory
	NewPeer   PeerFactory

	OnStatus      func(Status)
	OnRemoteFrame func(media.I420Frame)
	OnError       func(error)

	// HelloTimeout bounds how long a connected page may take to send hello.
	HelloTimeout time.Duration
}

// Session pairs with one phone at a time.
type Session struct {
	cfg Config

	mu         sync.Mutex
	status     Status
	runCancel  context.CancelFunc
	connCancel context.CancelFunc
	server     SignalingServer
	gen        int // identifies the current server run

	connected atomic.Bool
	latest    atomic.Pointer[media.RGBAFrame]
	notify    chan struct{}
}

// NewSession creates a stopped session.
func NewSession(cfg Config) (*Session, error) {
	if cfg.NewServer == nil || cfg.NewPeer == nil {
		return nil, errors.New("SessionConfigInvalid", "NewServer and NewPeer are required")
	}
	if cfg.HelloTimeout == 0 {
		cfg.HelloTimeout = 30 * time.Second
	}
	return &Session{cfg: cfg, notify: make(chan struct{}, 1)}, nil
}

// Status returns the current status.
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// WantsFrames reports whether camera frames are being consumed, so callers
// can skip copying them when no phone is connected.
func (s *Session) WantsFrames() bool { return s.connected.Load() }

// PushFrame offers the latest camera frame. It never blocks; an unsent
// previous frame is replaced.
func (s *Session) PushFrame(f media.RGBAFrame) {
	s.latest.Store(&f)
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Start (re)starts the signaling server on ip and waits for a phone. A
// phone that is currently connected is disconnected.
func (s *Session) Start(ctx context.Context, ip net.IP) error {
	s.Stop()

	s.mu.Lock()
	s.gen++
	gen := s.gen
	s.mu.Unlock()

	srv, err := s.cfg.NewServer(ip, func(connCtx context.Context, c signaling.Conn) { s.handleConn(connCtx, gen, c) })
	if err != nil {
		return err
	}
	if err := srv.Listen(ctx); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)

	s.mu.Lock()
	s.server, s.runCancel = srv, cancel
	s.mu.Unlock()
	s.setStatus(Status{State: StateWaitingForPhone, URL: srv.URL()})

	go func() {
		if err := srv.Serve(runCtx); err != nil {
			s.reportError(err)
		}
	}()
	return nil
}

// Stop shuts the server and any phone connection down.
func (s *Session) Stop() {
	s.mu.Lock()
	cancel := s.runCancel
	s.runCancel, s.connCancel, s.server = nil, nil, nil
	s.gen++
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		s.connected.Store(false)
		s.setStatus(Status{State: StateStopped})
	}
}

// NewPairing disconnects the current phone (if any) and issues a new QR URL.
func (s *Session) NewPairing() error {
	s.mu.Lock()
	cancel, srv := s.connCancel, s.server
	s.mu.Unlock()
	if srv == nil {
		return errors.New("SessionNotStarted", "the session is not running")
	}
	if cancel != nil {
		cancel() // handleConn rotates the token on its way out
		return nil
	}
	url, err := srv.Rotate()
	if err != nil {
		return err
	}
	s.setStatus(Status{State: StateWaitingForPhone, URL: url})
	return nil
}

func (s *Session) handleConn(ctx context.Context, gen int, c signaling.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.mu.Lock()
	srv := s.server
	if gen != s.gen || srv == nil {
		s.mu.Unlock()
		return
	}
	s.connCancel = cancel
	s.mu.Unlock()

	defer func() {
		s.connected.Store(false)
		s.mu.Lock()
		stale := gen != s.gen
		if !stale {
			s.connCancel = nil
		}
		s.mu.Unlock()
		if stale {
			return
		}
		url, err := srv.Rotate()
		if err != nil {
			s.reportError(err)
			return
		}
		s.setStatus(Status{State: StateWaitingForPhone, URL: url})
	}()

	if err := s.negotiate(ctx, cancel, gen, c); err != nil && ctx.Err() == nil {
		s.reportError(err)
	}
}

// negotiate runs one phone connection until it ends.
func (s *Session) negotiate(ctx context.Context, cancel context.CancelFunc, gen int, c signaling.Conn) error {
	hello, err := receiveWithTimeout(ctx, c, s.cfg.HelloTimeout)
	if err != nil {
		return err
	}
	if hello.Type != signaling.TypeHello {
		return errors.New("UnexpectedMessage", "expected hello", "type", hello.Type)
	}
	phone := c.UserAgent()
	if hello.UserAgent != "" {
		phone = hello.UserAgent
	}
	s.setStatusIfCurrent(gen, func(st Status) Status { return Status{State: StateNegotiating, URL: st.URL, Phone: phone} })

	peer, err := s.cfg.NewPeer(PeerCallbacks{
		OnLocalCandidate: func(ci webrtc.ICECandidateInit) {
			if err := c.Send(ctx, candidateMessage(ci)); err != nil && ctx.Err() == nil {
				s.reportError(err)
			}
		},
		OnState: func(st rtc.State) {
			switch st {
			case rtc.StateConnected:
				s.connected.Store(true)
				s.setStatusIfCurrent(gen, func(cur Status) Status { return Status{State: StateConnected, URL: cur.URL, Phone: phone} })
			case rtc.StateFailed, rtc.StateClosed:
				cancel()
			}
		},
		OnFrame: s.cfg.OnRemoteFrame,
		OnError: s.reportError,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := peer.Close(); err != nil {
			s.reportError(err)
		}
	}()

	go s.pumpFrames(ctx, peer)

	offer, err := peer.CreateOffer(ctx)
	if err != nil {
		return err
	}
	if err := c.Send(ctx, signaling.Message{Type: signaling.TypeOffer, SDP: offer}); err != nil {
		return err
	}

	for {
		msg, err := c.Receive(ctx)
		if err != nil {
			return err
		}
		switch msg.Type {
		case signaling.TypeAnswer:
			if err := peer.SetAnswer(ctx, msg.SDP); err != nil {
				return err
			}
		case signaling.TypeCandidate:
			if err := peer.AddRemoteCandidate(candidateInit(msg)); err != nil {
				return err
			}
		case signaling.TypeBye:
			return nil
		}
	}
}

// pumpFrames converts and sends the newest camera frame whenever one arrives.
func (s *Session) pumpFrames(ctx context.Context, peer Peer) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.notify:
		}
		if !s.connected.Load() {
			continue
		}
		f := s.latest.Swap(nil)
		if f == nil {
			continue
		}
		i420, err := media.RGBAToI420(media.ShrinkRGBA(*f, maxSendWidth))
		if err == nil {
			err = peer.WriteFrame(ctx, i420)
		}
		if err != nil && ctx.Err() == nil {
			s.reportError(err)
		}
	}
}

func receiveWithTimeout(ctx context.Context, c signaling.Conn, d time.Duration) (signaling.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return c.Receive(ctx)
}

func (s *Session) setStatus(st Status) {
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
	if s.cfg.OnStatus != nil {
		s.cfg.OnStatus(st)
	}
}

// setStatusIfCurrent updates the status unless the server run was replaced.
func (s *Session) setStatusIfCurrent(gen int, update func(Status) Status) {
	s.mu.Lock()
	if gen != s.gen {
		s.mu.Unlock()
		return
	}
	s.status = update(s.status)
	st := s.status
	s.mu.Unlock()
	if s.cfg.OnStatus != nil {
		s.cfg.OnStatus(st)
	}
}

func (s *Session) reportError(err error) {
	if s.cfg.OnError != nil {
		s.cfg.OnError(err)
	}
}

func candidateMessage(c webrtc.ICECandidateInit) signaling.Message {
	return signaling.Message{
		Type:             signaling.TypeCandidate,
		Candidate:        c.Candidate,
		SDPMid:           c.SDPMid,
		SDPMLineIndex:    c.SDPMLineIndex,
		UsernameFragment: c.UsernameFragment,
	}
}

func candidateInit(m signaling.Message) webrtc.ICECandidateInit {
	return webrtc.ICECandidateInit{
		Candidate:        m.Candidate,
		SDPMid:           m.SDPMid,
		SDPMLineIndex:    m.SDPMLineIndex,
		UsernameFragment: m.UsernameFragment,
	}
}
