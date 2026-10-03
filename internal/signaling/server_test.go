package signaling_test

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/signaling"
	"github.com/rm4n0s/desktop-mirror/internal/tlscert"
)

type testServer struct {
	srv       *signaling.Server
	client    *http.Client
	base      string // https://127.0.0.1:port
	connected chan signaling.Conn
	release   chan struct{}
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cert, err := tlscert.LoadOrCreate(ctx, "", []net.IP{net.IPv4(127, 0, 0, 1)}, time.Now())
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	ts := &testServer{connected: make(chan signaling.Conn, 4), release: make(chan struct{})}
	srv, err := signaling.NewServer(signaling.Config{
		IP:   net.IPv4(127, 0, 0, 1),
		Cert: cert,
		Static: fstest.MapFS{
			"index.html": {Data: []byte("<html>pairing page</html>")},
			"app.js":     {Data: []byte("console.log('hi')")},
			"secret.txt": {Data: []byte("nope")},
		},
		OnConnect: func(ctx context.Context, c signaling.Conn) {
			ts.connected <- c
			select {
			case <-ts.release:
			case <-ctx.Done():
			}
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := srv.Listen(ctx); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = srv.Serve(ctx) }()

	ts.srv = srv
	ts.base = "https://" + srv.Addr().String()
	ts.client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	return ts
}

func (ts *testServer) token(t *testing.T) string {
	t.Helper()
	parts := strings.Split(strings.Trim(ts.srv.URL(), "/"), "/")
	return parts[len(parts)-1]
}

func (ts *testServer) get(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := ts.client.Get(ts.base + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (ts *testServer) dial(ctx context.Context, token string) (*websocket.Conn, *http.Response, error) {
	return websocket.Dial(ctx, "wss://"+ts.srv.Addr().String()+"/s/"+token+"/ws", &websocket.DialOptions{HTTPClient: ts.client})
}

func TestServerServesPageOnlyWithValidToken(t *testing.T) {
	ts := newTestServer(t)
	tok := ts.token(t)
	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"index", "/s/" + tok + "/", http.StatusOK, "pairing page"},
		{"script", "/s/" + tok + "/app.js", http.StatusOK, "console.log"},
		{"missing file", "/s/" + tok + "/nope.js", http.StatusNotFound, ""},
		{"wrong token", "/s/wrong/", http.StatusNotFound, ""},
		{"wrong token file", "/s/wrong/app.js", http.StatusNotFound, ""},
		{"no token", "/", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := ts.get(t, tt.path)
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			if !strings.Contains(body, tt.wantBody) {
				t.Errorf("body %q does not contain %q", body, tt.wantBody)
			}
		})
	}
}

func TestServerWebSocketPairing(t *testing.T) {
	ts := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok := ts.token(t)

	c, _, err := ts.dial(ctx, tok)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	var conn signaling.Conn
	select {
	case conn = <-ts.connected:
	case <-ctx.Done():
		t.Fatal("OnConnect was not called")
	}

	// phone -> desktop
	if err := wsjson.Write(ctx, c, signaling.Message{Type: signaling.TypeHello, UserAgent: "TestPhone"}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	got, err := conn.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if got.Type != signaling.TypeHello || got.UserAgent != "TestPhone" {
		t.Errorf("got %+v", got)
	}

	// desktop -> phone
	if err := conn.Send(ctx, signaling.Message{Type: signaling.TypeOffer, SDP: "v=0"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var reply signaling.Message
	if err := wsjson.Read(ctx, c, &reply); err != nil {
		t.Fatalf("read offer: %v", err)
	}
	if reply.Type != signaling.TypeOffer || reply.SDP != "v=0" {
		t.Errorf("reply = %+v", reply)
	}

	// invalid message from the phone is rejected by Receive
	if err := wsjson.Write(ctx, c, signaling.Message{Type: "explode"}); err != nil {
		t.Fatalf("write bad message: %v", err)
	}
	_, err = conn.Receive(ctx)
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("wsConn.Receive->Message.Validate.SignalingMessageInvalid") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}

	// a second phone with the same token is told the session is busy
	c2, _, err := ts.dial(ctx, tok)
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	defer c2.CloseNow()
	var busy signaling.Message
	if err := wsjson.Read(ctx, c2, &busy); err != nil {
		t.Fatalf("read busy: %v", err)
	}
	if busy.Type != signaling.TypeError || busy.Code != signaling.CodeSessionBusy {
		t.Errorf("busy = %+v", busy)
	}

	// the page is no longer served for a claimed token
	if status, _ := ts.get(t, "/s/"+tok+"/"); status != http.StatusConflict {
		t.Errorf("page status = %d, want 409", status)
	}

	// rotating issues a fresh token and frees the server for a new phone
	close(ts.release)
	if _, err := ts.srv.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if status, _ := ts.get(t, "/s/"+tok+"/"); status != http.StatusNotFound {
		t.Errorf("old token status = %d, want 404", status)
	}
	if status, _ := ts.get(t, "/s/"+ts.token(t)+"/"); status != http.StatusOK {
		t.Errorf("new token status = %d, want 200", status)
	}
}

func TestServerRejectsUnknownTokenAndForeignOrigin(t *testing.T) {
	ts := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, resp, err := ts.dial(ctx, "wrong"); err == nil {
		t.Error("dial with a wrong token succeeded")
	} else if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("wrong token response = %v, want 404", resp)
	}

	_, resp, err := websocket.Dial(ctx, "wss://"+ts.srv.Addr().String()+"/s/"+ts.token(t)+"/ws", &websocket.DialOptions{
		HTTPClient: ts.client,
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err == nil {
		t.Error("dial with a foreign Origin succeeded")
	} else if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin response = %v, want 403", resp)
	}

	// The rejected attempts must not have consumed the pairing token.
	if status, _ := ts.get(t, "/s/"+ts.token(t)+"/"); status != http.StatusOK {
		t.Errorf("page status after rejected attempts = %d, want 200", status)
	}
}

func TestNewServerRejectsIncompleteConfig(t *testing.T) {
	_, err := signaling.NewServer(signaling.Config{})
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("NewServer.ServerConfigInvalid") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

func TestListenFailsWhenPortTaken(t *testing.T) {
	ts := newTestServer(t)
	port := ts.srv.Addr().(*net.TCPAddr).Port

	srv, err := signaling.NewServer(signaling.Config{
		IP: net.IPv4(127, 0, 0, 1), Port: port, Static: fstest.MapFS{},
		OnConnect: func(context.Context, signaling.Conn) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = srv.Listen(context.Background())
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("Server.Listen.ServerListenFailed") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}

	// With fallback enabled it picks another port instead.
	srv, err = signaling.NewServer(signaling.Config{
		IP: net.IPv4(127, 0, 0, 1), Port: port, FallbackToFreePort: true, Static: fstest.MapFS{},
		OnConnect: func(context.Context, signaling.Conn) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Listen(context.Background()); err != nil {
		t.Fatalf("Listen with fallback: %v", err)
	}
	if got := srv.Addr().(*net.TCPAddr).Port; got == port || got == 0 {
		t.Errorf("fallback port = %d", got)
	}
}
