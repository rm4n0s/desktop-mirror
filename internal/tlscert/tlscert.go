// Package tlscert creates and persists the self-signed certificate that lets
// phone browsers open the pairing page over HTTPS (required for getUserMedia).
package tlscert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/rm4n0s/errors"
)

const (
	certFile = "cert.pem"
	keyFile  = "key.pem"
	validity = 365 * 24 * time.Hour
	// renewBefore regenerates a certificate that is about to expire.
	renewBefore = 30 * 24 * time.Hour
)

// LoadOrCreate returns the certificate stored in dir, generating a new one
// when none exists, it is about to expire, or it does not cover all ips.
func LoadOrCreate(ctx context.Context, dir string, ips []net.IP, now time.Time) (tls.Certificate, error) {
	if err := ctx.Err(); err != nil {
		return tls.Certificate{}, errors.NewErr("CertContextCancelled", err)
	}
	if cert, ok := load(dir, ips, now); ok {
		return cert, nil
	}
	return create(dir, ips, now)
}

func load(dir string, ips []net.IP, now time.Time) (tls.Certificate, bool) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, certFile), filepath.Join(dir, keyFile))
	if err != nil {
		return tls.Certificate{}, false
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, false
	}
	if now.Add(renewBefore).After(leaf.NotAfter) || now.Before(leaf.NotBefore) {
		return tls.Certificate{}, false
	}
	for _, ip := range ips {
		if err := leaf.VerifyHostname(ip.String()); err != nil {
			return tls.Certificate{}, false
		}
	}
	cert.Leaf = leaf
	return cert, true
}

func create(dir string, ips []net.IP, now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, errors.NewErr("CertGenerationFailed", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, errors.NewErr("CertGenerationFailed", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "desktop-mirror", Organization: []string{"desktop-mirror"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           append([]net.IP{net.IPv4(127, 0, 0, 1)}, ips...),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, errors.NewErr("CertGenerationFailed", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, errors.NewErr("CertGenerationFailed", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, errors.NewErr("CertGenerationFailed", err)
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return tls.Certificate{}, errors.NewErr("CertStorageFailed", err).SetMetadata("dir", dir)
		}
		if err := os.WriteFile(filepath.Join(dir, certFile), certPEM, 0o600); err != nil {
			return tls.Certificate{}, errors.NewErr("CertStorageFailed", err).SetMetadata("dir", dir)
		}
		if err := os.WriteFile(filepath.Join(dir, keyFile), keyPEM, 0o600); err != nil {
			return tls.Certificate{}, errors.NewErr("CertStorageFailed", err).SetMetadata("dir", dir)
		}
	}
	return cert, nil
}
