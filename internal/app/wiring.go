package app

import (
	"context"
	"crypto/tls"
	"io/fs"
	"net"

	"github.com/rm4n0s/desktop-mirror/internal/rtc"
	"github.com/rm4n0s/desktop-mirror/internal/signaling"
)

// NewServerFactory returns the production ServerFactory: an HTTPS server on
// port (falling back to a free one) that serves site to phones. certFor
// supplies the certificate for the interface address being served.
func NewServerFactory(certFor func(ip net.IP) (tls.Certificate, error), site fs.FS, port int) ServerFactory {
	return func(ip net.IP, onConnect func(context.Context, signaling.Conn)) (SignalingServer, error) {
		cert, err := certFor(ip)
		if err != nil {
			return nil, err
		}
		srv, err := signaling.NewServer(signaling.Config{
			IP:                 ip,
			Port:               port,
			FallbackToFreePort: true,
			Cert:               cert,
			Static:             site,
			OnConnect:          onConnect,
		})
		if err != nil {
			return nil, err
		}
		return srv, nil
	}
}

// NewPeerFactory returns the production PeerFactory. base supplies the codec
// factories and network settings; the callbacks are filled in per peer.
func NewPeerFactory(base rtc.Config) PeerFactory {
	return func(cb PeerCallbacks) (Peer, error) {
		cfg := base
		cfg.OnLocalCandidate = cb.OnLocalCandidate
		cfg.OnState = cb.OnState
		cfg.OnFrame = cb.OnFrame
		cfg.OnError = cb.OnError
		p, err := rtc.NewPeer(cfg)
		if err != nil {
			return nil, err
		}
		return p, nil
	}
}
