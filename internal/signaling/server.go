package signaling

import (
	"context"
	"crypto/tls"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/rm4n0s/errors"
)

const (
	maxMessageBytes = 64 << 10
	writeTimeout    = 10 * time.Second
)

// Conn is one phone's signaling channel.
type Conn interface {
	Send(ctx context.Context, m Message) error
	// Receive blocks for the next valid message.
	Receive(ctx context.Context) (Message, error)
	UserAgent() string
}

// Config configures a Server.
type Config struct {
	IP   net.IP
	Port int
	// FallbackToFreePort retries on an OS-assigned port if Port is taken.
	FallbackToFreePort bool
	Cert               tls.Certificate
	// Static is the site served to the phone (index.html, app.js, ...).
	Static   fs.FS
	TokenTTL time.Duration
	Now      func() time.Time
	// OnConnect is called, in its own goroutine, for each phone that
	// presents a valid token. The WebSocket closes when it returns.
	OnConnect func(ctx context.Context, c Conn)
}

// Server serves the pairing page over HTTPS and accepts WebSocket
// connections that carry a valid pairing token.
type Server struct {
	cfg    Config
	tokens *TokenManager
	ln     net.Listener
	srv    *http.Server
}

// NewServer validates cfg and issues the first pairing token.
func NewServer(cfg Config) (*Server, error) {
	if cfg.IP == nil || cfg.Static == nil || cfg.OnConnect == nil {
		return nil, errors.New("ServerConfigInvalid", "IP, Static and OnConnect are required")
	}
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = 10 * time.Minute
	}
	tokens, err := NewTokenManager(cfg.TokenTTL, cfg.Now)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, tokens: tokens}, nil
}

// Listen binds the TCP port. Call before Serve.
func (s *Server) Listen(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp4", net.JoinHostPort(s.cfg.IP.String(), strconv.Itoa(s.cfg.Port)))
	if err != nil && s.cfg.FallbackToFreePort && s.cfg.Port != 0 {
		ln, err = lc.Listen(ctx, "tcp4", net.JoinHostPort(s.cfg.IP.String(), "0"))
	}
	if err != nil {
		return errors.NewErr("ServerListenFailed", err).SetMetadata("ip", s.cfg.IP.String(), "port", s.cfg.Port)
	}
	s.ln = ln
	return nil
}

// Addr is the bound address; valid after Listen.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// URL is the pairing URL to put in the QR code.
func (s *Server) URL() string {
	return "https://" + s.ln.Addr().String() + "/s/" + s.tokens.Current() + "/"
}

// Rotate invalidates the current token and returns the new pairing URL.
func (s *Server) Rotate() (string, error) {
	if _, err := s.tokens.Rotate(); err != nil {
		return "", err
	}
	return s.URL(), nil
}

// Serve handles requests until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /s/{token}/{$}", s.handlePage)
	mux.HandleFunc("GET /s/{token}/ws", func(w http.ResponseWriter, r *http.Request) { s.handleWS(ctx, w, r) })
	mux.HandleFunc("GET /s/{token}/{file}", s.handleFile)

	s.srv = &http.Server{
		Handler:           noStore(mux),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{s.cfg.Cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			_ = s.srv.Close()
		}
	}()
	err := s.srv.ServeTLS(s.ln, "", "")
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.NewErr("ServeFailed", err)
	}
	return nil
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// httpStatus maps a token error to the status served for plain requests.
func httpStatus(err error) int {
	appErr, ok := errors.FromError(err)
	if !ok {
		return http.StatusInternalServerError
	}
	switch appErr.Tag {
	case "TokenInvalid":
		return http.StatusNotFound
	case "TokenExpired":
		return http.StatusGone
	case "SessionBusy":
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if err := s.tokens.Check(r.PathValue("token")); err != nil {
		http.Error(w, err.Error()+". Scan the QR code in the desktop app again.", httpStatus(err))
		return
	}
	s.serveStatic(w, r, "index.html")
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	if err := s.tokens.Check(r.PathValue("token")); err != nil {
		http.Error(w, err.Error(), httpStatus(err))
		return
	}
	s.serveStatic(w, r, r.PathValue("file"))
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request, name string) {
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	f, err := s.cfg.Static.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "static file is not seekable", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), rs)
}

func (s *Server) handleWS(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	claimErr := s.tokens.Claim(token)
	if appErr, ok := errors.FromError(claimErr); ok && appErr.Tag == "TokenInvalid" {
		http.NotFound(w, r)
		return
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept already wrote the HTTP error response (e.g. bad Origin).
		if claimErr == nil {
			_, _ = s.tokens.Rotate()
		}
		return
	}
	c.SetReadLimit(maxMessageBytes)

	if claimErr != nil {
		code := CodeSessionBusy
		if appErr, ok := errors.FromError(claimErr); ok && appErr.Tag == "TokenExpired" {
			code = CodeTokenExpire
		}
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		_ = wsjson.Write(wctx, c, Message{Type: TypeError, Code: code, Text: claimErr.Error()})
		cancel()
		_ = c.Close(websocket.StatusPolicyViolation, code)
		return
	}

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.cfg.OnConnect(connCtx, &wsConn{c: c, ua: r.UserAgent()})
	_ = c.Close(websocket.StatusNormalClosure, "")
}

type wsConn struct {
	c  *websocket.Conn
	ua string
}

func (w *wsConn) UserAgent() string { return w.ua }

func (w *wsConn) Send(ctx context.Context, m Message) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := wsjson.Write(ctx, w.c, m); err != nil {
		return errors.NewErr("SignalingSendFailed", err).SetMetadata("type", m.Type)
	}
	return nil
}

func (w *wsConn) Receive(ctx context.Context) (Message, error) {
	var m Message
	if err := wsjson.Read(ctx, w.c, &m); err != nil {
		return Message{}, errors.NewErr("SignalingReceiveFailed", err)
	}
	if err := m.Validate(); err != nil {
		return Message{}, err
	}
	return m, nil
}
