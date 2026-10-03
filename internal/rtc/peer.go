// Package rtc wraps a pion PeerConnection that exchanges one VP8 video track
// in each direction, hiding RTP packetization, RTCP feedback and codec use.
package rtc

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/intervalpli"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media"
)

const (
	defaultBitrate   = 1_500_000
	defaultFrameRate = 30
	// minPLIInterval limits how often loss triggers a keyframe request.
	minPLIInterval = time.Second
	videoClockRate = 90000
	vp8PayloadType = 96
)

// State is the coarse connection state reported to the application.
type State int

const (
	StateNew State = iota
	StateConnecting
	StateConnected
	StateDisconnected
	StateFailed
	StateClosed
)

func (s State) String() string {
	return [...]string{"new", "connecting", "connected", "disconnected", "failed", "closed"}[s]
}

// Config configures a Peer. The On* callbacks may be invoked from pion
// goroutines and must not block.
type Config struct {
	UDPPortMin, UDPPortMax uint16
	STUNServers            []string
	IncludeLoopback        bool
	Bitrate                int
	FrameRate              int

	EncoderFactory media.EncoderFactory
	DecoderFactory media.DecoderFactory

	OnLocalCandidate func(webrtc.ICECandidateInit)
	OnState          func(State)
	OnFrame          func(media.I420Frame)
	OnError          func(error)
}

// Peer is one WebRTC connection to a phone.
type Peer struct {
	cfg   Config
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticSample

	forceKeyframe atomic.Bool
	connected     atomic.Bool

	mu         sync.Mutex // guards the fields below
	encoder    media.Encoder
	encW, encH int
	lastWrite  time.Time
	remoteSet  bool
	pending    []webrtc.ICECandidateInit
	lastPLI    time.Time
	closed     bool
}

// NewPeer creates the PeerConnection with a sendrecv VP8 video transceiver.
func NewPeer(cfg Config) (*Peer, error) {
	if cfg.EncoderFactory == nil || cfg.DecoderFactory == nil {
		return nil, errors.New("PeerConfigInvalid", "EncoderFactory and DecoderFactory are required")
	}
	if cfg.Bitrate == 0 {
		cfg.Bitrate = defaultBitrate
	}
	if cfg.FrameRate == 0 {
		cfg.FrameRate = defaultFrameRate
	}

	me := &webrtc.MediaEngine{}
	err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeVP8,
			ClockRate: videoClockRate,
			RTCPFeedback: []webrtc.RTCPFeedback{
				{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"},
			},
		},
		PayloadType: vp8PayloadType,
	}, webrtc.RTPCodecTypeVideo)
	if err != nil {
		return nil, errors.NewErr("PeerCreateFailed", err)
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, ir); err != nil {
		return nil, errors.NewErr("PeerCreateFailed", err)
	}
	pli, err := intervalpli.NewReceiverInterceptor()
	if err != nil {
		return nil, errors.NewErr("PeerCreateFailed", err)
	}
	ir.Add(pli)

	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeQueryOnly)
	se.SetIncludeLoopbackCandidate(cfg.IncludeLoopback)
	if cfg.UDPPortMax != 0 {
		if err := se.SetEphemeralUDPPortRange(cfg.UDPPortMin, cfg.UDPPortMax); err != nil {
			return nil, errors.NewErr("PeerCreateFailed", err).SetMetadata("min", cfg.UDPPortMin, "max", cfg.UDPPortMax)
		}
	}

	var servers []webrtc.ICEServer
	if len(cfg.STUNServers) > 0 {
		servers = append(servers, webrtc.ICEServer{URLs: cfg.STUNServers})
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if err != nil {
		return nil, errors.NewErr("PeerCreateFailed", err)
	}

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: videoClockRate}, "video", "desktop-mirror")
	if err != nil {
		_ = pc.Close()
		return nil, errors.NewErr("PeerCreateFailed", err)
	}
	tr, err := pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv})
	if err != nil {
		_ = pc.Close()
		return nil, errors.NewErr("PeerCreateFailed", err)
	}

	p := &Peer{cfg: cfg, pc: pc, track: track}
	p.forceKeyframe.Store(true)

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if p.cfg.OnLocalCandidate == nil {
			return
		}
		if c == nil {
			p.cfg.OnLocalCandidate(webrtc.ICECandidateInit{})
			return
		}
		p.cfg.OnLocalCandidate(c.ToJSON())
	})
	pc.OnConnectionStateChange(p.handleConnectionState)
	pc.OnTrack(p.handleTrack)
	go p.readSenderRTCP(tr.Sender())
	return p, nil
}

// CreateOffer creates the local offer and starts ICE gathering (trickle).
func (p *Peer) CreateOffer(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", errors.NewErr("OfferFailed", err)
	}
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return "", errors.NewErr("OfferFailed", err)
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return "", errors.NewErr("OfferFailed", err)
	}
	return offer.SDP, nil
}

// SetAnswer applies the remote answer and flushes queued candidates.
func (p *Peer) SetAnswer(ctx context.Context, sdp string) error {
	if err := ctx.Err(); err != nil {
		return errors.NewErr("RemoteDescriptionFailed", err)
	}
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		return errors.NewErr("RemoteDescriptionFailed", err)
	}
	return p.flushPending()
}

// AcceptOffer applies a remote offer and returns the local answer. It lets a
// pion peer play the phone's role (used by tests and tools).
func (p *Peer) AcceptOffer(ctx context.Context, sdp string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", errors.NewErr("AnswerFailed", err)
	}
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		return "", errors.NewErr("RemoteDescriptionFailed", err)
	}
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return "", errors.NewErr("AnswerFailed", err)
	}
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return "", errors.NewErr("AnswerFailed", err)
	}
	if err := p.flushPending(); err != nil {
		return "", err
	}
	return answer.SDP, nil
}

// AddRemoteCandidate adds a trickled candidate, queueing it until the remote
// description is set. An empty candidate (end of candidates) is ignored.
func (p *Peer) AddRemoteCandidate(c webrtc.ICECandidateInit) error {
	if c.Candidate == "" {
		return nil
	}
	p.mu.Lock()
	if !p.remoteSet {
		p.pending = append(p.pending, c)
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	if err := p.pc.AddICECandidate(c); err != nil {
		return errors.NewErr("CandidateRejected", err)
	}
	return nil
}

func (p *Peer) flushPending() error {
	p.mu.Lock()
	p.remoteSet = true
	pending := p.pending
	p.pending = nil
	p.mu.Unlock()
	for _, c := range pending {
		if err := p.pc.AddICECandidate(c); err != nil {
			return errors.NewErr("CandidateRejected", err)
		}
	}
	return nil
}

// WriteFrame encodes f and sends it. It is a no-op until the connection is
// up. Call from a single goroutine.
func (p *Peer) WriteFrame(ctx context.Context, f media.I420Frame) error {
	if !p.connected.Load() {
		return nil
	}
	if !f.Valid() {
		return errors.New("InvalidFrameSize", "cannot send an invalid frame", "width", f.Width, "height", f.Height)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if p.encoder == nil || p.encW != f.Width || p.encH != f.Height {
		if p.encoder != nil {
			_ = p.encoder.Close()
			p.encoder = nil
		}
		enc, err := p.cfg.EncoderFactory(f.Width, f.Height, p.cfg.Bitrate)
		if err != nil {
			return err
		}
		p.encoder, p.encW, p.encH = enc, f.Width, f.Height
		p.forceKeyframe.Store(true)
	}
	au, err := p.encoder.Encode(ctx, f, p.forceKeyframe.Swap(false))
	if err != nil {
		return err
	}
	if len(au) == 0 {
		return nil
	}
	now := time.Now()
	d := time.Second / time.Duration(p.cfg.FrameRate)
	if !p.lastWrite.IsZero() {
		d = min(max(now.Sub(p.lastWrite), 5*time.Millisecond), 250*time.Millisecond)
	}
	p.lastWrite = now
	if err := p.track.WriteSample(pionmedia.Sample{Data: au, Duration: d}); err != nil {
		return errors.NewErr("SendSampleFailed", err)
	}
	return nil
}

// Close tears down the connection and releases the encoder.
func (p *Peer) Close() error {
	p.mu.Lock()
	p.closed = true
	enc := p.encoder
	p.encoder = nil
	p.mu.Unlock()
	if enc != nil {
		_ = enc.Close()
	}
	if err := p.pc.Close(); err != nil {
		return errors.NewErr("PeerCloseFailed", err)
	}
	return nil
}

func (p *Peer) handleConnectionState(s webrtc.PeerConnectionState) {
	var st State
	switch s {
	case webrtc.PeerConnectionStateConnecting:
		st = StateConnecting
	case webrtc.PeerConnectionStateConnected:
		st = StateConnected
		p.forceKeyframe.Store(true)
	case webrtc.PeerConnectionStateDisconnected:
		st = StateDisconnected
	case webrtc.PeerConnectionStateFailed:
		st = StateFailed
	case webrtc.PeerConnectionStateClosed:
		st = StateClosed
	default:
		return
	}
	p.connected.Store(st == StateConnected)
	if p.cfg.OnState != nil {
		p.cfg.OnState(st)
	}
}

// readSenderRTCP serves feedback from the phone: keyframe requests and (via
// the interceptors) NACKs.
func (p *Peer) readSenderRTCP(sender *webrtc.RTPSender) {
	for {
		pkts, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, pkt := range pkts {
			switch pkt.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				p.forceKeyframe.Store(true)
			}
		}
	}
}

func (p *Peer) handleTrack(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	if remote.Kind() != webrtc.RTPCodecTypeVideo || remote.Codec().MimeType != webrtc.MimeTypeVP8 {
		return
	}
	go p.readRemote(remote)
}

func (p *Peer) readRemote(remote *webrtc.TrackRemote) {
	ctx := context.Background()
	dec, err := p.cfg.DecoderFactory()
	if err != nil {
		p.reportError(err)
		return
	}
	defer func() { _ = dec.Close() }()

	sb := samplebuilder.New(64, &codecs.VP8Packet{}, remote.Codec().ClockRate)
	var lastSeq uint16
	haveSeq := false
	for {
		pkt, _, err := remote.ReadRTP()
		if err != nil {
			return
		}
		if haveSeq {
			// A forward jump of more than one means packets were lost
			// (late retransmits have a negative diff and are ignored).
			if diff := int16(pkt.SequenceNumber - lastSeq); diff > 1 {
				p.requestKeyframe(remote.SSRC())
			} else if diff <= 0 {
				// Reordered or retransmitted packet: still feed the builder.
				sb.Push(pkt)
				p.drain(ctx, sb, dec, remote.SSRC())
				continue
			}
		}
		lastSeq, haveSeq = pkt.SequenceNumber, true
		p.pushAndDrain(ctx, sb, pkt, dec, remote.SSRC())
	}
}

func (p *Peer) pushAndDrain(ctx context.Context, sb *samplebuilder.SampleBuilder, pkt *rtp.Packet, dec media.Decoder, ssrc webrtc.SSRC) {
	sb.Push(pkt)
	p.drain(ctx, sb, dec, ssrc)
}

func (p *Peer) drain(ctx context.Context, sb *samplebuilder.SampleBuilder, dec media.Decoder, ssrc webrtc.SSRC) {
	for s := sb.Pop(); s != nil; s = sb.Pop() {
		frame, ok, err := dec.Decode(ctx, s.Data)
		if err != nil {
			p.reportError(err)
			p.requestKeyframe(ssrc)
			continue
		}
		if ok && p.cfg.OnFrame != nil {
			p.cfg.OnFrame(frame)
		}
	}
}

func (p *Peer) requestKeyframe(ssrc webrtc.SSRC) {
	p.mu.Lock()
	now := time.Now()
	if now.Sub(p.lastPLI) < minPLIInterval {
		p.mu.Unlock()
		return
	}
	p.lastPLI = now
	p.mu.Unlock()
	if err := p.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)}}); err != nil {
		p.reportError(errors.NewErr("KeyframeRequestFailed", err))
	}
}

func (p *Peer) reportError(err error) {
	if p.cfg.OnError != nil {
		p.cfg.OnError(err)
	}
}
