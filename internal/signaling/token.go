package signaling

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"

	"github.com/rm4n0s/errors"
)

// TokenManager holds the single pairing token embedded in the QR code. It is
// valid for ttl while unclaimed and is claimed by the first WebSocket.
type TokenManager struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	token   string
	issued  time.Time
	claimed bool
}

// NewTokenManager issues a first token. now may be nil to use time.Now.
func NewTokenManager(ttl time.Duration, now func() time.Time) (*TokenManager, error) {
	if now == nil {
		now = time.Now
	}
	m := &TokenManager{ttl: ttl, now: now}
	if _, err := m.Rotate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Current returns the active token.
func (m *TokenManager) Current() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token
}

// Rotate replaces the token with a fresh unclaimed one and returns it.
func (m *TokenManager) Rotate() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.NewErr("TokenGenerationFailed", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = base64.RawURLEncoding.EncodeToString(buf)
	m.issued = m.now()
	m.claimed = false
	return m.token, nil
}

// Check reports whether tok is the current, unclaimed, unexpired token.
func (m *TokenManager) Check(tok string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.check(tok)
}

// Claim is Check followed by marking the token as used.
func (m *TokenManager) Claim(tok string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(tok); err != nil {
		return err
	}
	m.claimed = true
	return nil
}

func (m *TokenManager) check(tok string) error {
	if subtle.ConstantTimeCompare([]byte(tok), []byte(m.token)) != 1 {
		return errors.New("TokenInvalid", "unknown pairing token")
	}
	if m.claimed {
		return errors.New("SessionBusy", "pairing token already in use")
	}
	if m.now().Sub(m.issued) > m.ttl {
		return errors.New("TokenExpired", "pairing token expired")
	}
	return nil
}
