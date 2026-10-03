package signaling_test

import (
	"testing"
	"time"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/signaling"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestTokenManager(t *testing.T) {
	tests := []struct {
		name    string
		act     func(m *signaling.TokenManager, c *fakeClock) error
		wantTag string
	}{
		{"current token is valid", func(m *signaling.TokenManager, _ *fakeClock) error { return m.Check(m.Current()) }, ""},
		{"wrong token", func(m *signaling.TokenManager, _ *fakeClock) error { return m.Check("nope") }, "TokenInvalid"},
		{"empty token", func(m *signaling.TokenManager, _ *fakeClock) error { return m.Check("") }, "TokenInvalid"},
		{"expired", func(m *signaling.TokenManager, c *fakeClock) error {
			c.t = c.t.Add(11 * time.Minute)
			return m.Check(m.Current())
		}, "TokenExpired"},
		{"valid just before expiry", func(m *signaling.TokenManager, c *fakeClock) error {
			c.t = c.t.Add(9 * time.Minute)
			return m.Check(m.Current())
		}, ""},
		{"claimed token is busy", func(m *signaling.TokenManager, _ *fakeClock) error {
			if err := m.Claim(m.Current()); err != nil {
				return err
			}
			return m.Claim(m.Current())
		}, "SessionBusy"},
		{"claimed token is not expired by time", func(m *signaling.TokenManager, c *fakeClock) error {
			if err := m.Claim(m.Current()); err != nil {
				return err
			}
			c.t = c.t.Add(time.Hour)
			return m.Check(m.Current())
		}, "SessionBusy"},
		{"rotation invalidates the old token", func(m *signaling.TokenManager, _ *fakeClock) error {
			old := m.Current()
			if _, err := m.Rotate(); err != nil {
				return err
			}
			return m.Check(old)
		}, "TokenInvalid"},
		{"rotation releases a claim", func(m *signaling.TokenManager, _ *fakeClock) error {
			if err := m.Claim(m.Current()); err != nil {
				return err
			}
			if _, err := m.Rotate(); err != nil {
				return err
			}
			return m.Claim(m.Current())
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			m, err := signaling.NewTokenManager(10*time.Minute, clock.now)
			if err != nil {
				t.Fatalf("NewTokenManager: %v", err)
			}
			err = tt.act(m, clock)
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T (%v)", err, err)
			}
			if appErr.Tag != tt.wantTag {
				t.Errorf("tag = %s, want %s", appErr.Tag, tt.wantTag)
			}
			if !appErr.HasRoute("." + tt.wantTag) {
				t.Errorf("unexpected failure path: %s", appErr.Route())
			}
		})
	}
}

func TestTokensAreUnique(t *testing.T) {
	m, err := signaling.NewTokenManager(time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{m.Current(): true}
	for i := 0; i < 100; i++ {
		tok, err := m.Rotate()
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatalf("token %q repeated", tok)
		}
		if len(tok) < 22 {
			t.Fatalf("token %q has less than 128 bits", tok)
		}
		seen[tok] = true
	}
}
