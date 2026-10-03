package tlscert_test

import (
	"context"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/tlscert"
)

var now = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func leaf(t *testing.T, ips []net.IP, dir string, at time.Time) *x509.Certificate {
	t.Helper()
	cert, err := tlscert.LoadOrCreate(context.Background(), dir, ips, at)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	l, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return l
}

func TestCertCoversRequestedIPs(t *testing.T) {
	ips := []net.IP{net.IPv4(192, 168, 1, 20), net.IPv4(10, 0, 0, 5)}
	l := leaf(t, ips, "", now)
	for _, ip := range append(ips, net.IPv4(127, 0, 0, 1)) {
		if err := l.VerifyHostname(ip.String()); err != nil {
			t.Errorf("certificate does not cover %s: %v", ip, err)
		}
	}
	if err := l.VerifyHostname("172.16.0.9"); err == nil {
		t.Error("certificate unexpectedly covers an unrequested IP")
	}
}

func TestCertIsReusedAndRegenerated(t *testing.T) {
	dir := t.TempDir()
	ipA := []net.IP{net.IPv4(192, 168, 1, 20)}
	first := leaf(t, ipA, dir, now)

	tests := []struct {
		name      string
		ips       []net.IP
		at        time.Time
		wantReuse bool
	}{
		{"same IP is reused", ipA, now.Add(24 * time.Hour), true},
		{"new IP regenerates", []net.IP{net.IPv4(192, 168, 1, 99)}, now, false},
		{"near expiry regenerates", ipA, now.Add(340 * 24 * time.Hour), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each case starts from the same stored certificate.
			caseDir := t.TempDir()
			for _, f := range []string{"cert.pem", "key.pem"} {
				data, err := os.ReadFile(filepath.Join(dir, f))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(caseDir, f), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := leaf(t, tt.ips, caseDir, tt.at)
			if reused := got.SerialNumber.Cmp(first.SerialNumber) == 0; reused != tt.wantReuse {
				t.Errorf("reused = %v, want %v", reused, tt.wantReuse)
			}
		})
	}
}

func TestStoredKeyIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	leaf(t, nil, dir, now)
	info, err := os.Stat(filepath.Join(dir, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestStorageFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory path below a regular file cannot be created.
	_, err := tlscert.LoadOrCreate(context.Background(), filepath.Join(file, "sub"), nil, now)
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("LoadOrCreate->create.CertStorageFailed") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}

func TestCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tlscert.LoadOrCreate(ctx, "", nil, now)
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("LoadOrCreate.CertContextCancelled") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}
