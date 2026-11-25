package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/Faqihyugos/monstack-go/internal/config"
)

// Manager constructs tls.Config instances for the HTTP server based on TLSConfig.
type Manager struct {
	cfg      config.TLSConfig
	autocert *autocert.Manager
}

// NewManager creates a new TLS Manager.
func NewManager(cfg config.TLSConfig) *Manager {
	return &Manager{cfg: cfg}
}

// TLSConfig returns a configured *tls.Config for the selected mode (self-signed, manual, or acme).
func (m *Manager) TLSConfig() (*tls.Config, error) {
	mode := strings.ToLower(strings.TrimSpace(m.cfg.Mode))
	if mode == "" {
		mode = "self-signed"
	}

	switch mode {
	case "manual":
		cert, err := tls.LoadX509KeyPair(m.cfg.Manual.CertFile, m.cfg.Manual.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load manual TLS certificate: %w", err)
		}
		return &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}, nil

	case "acme":
		if len(m.cfg.ACME.Domains) == 0 {
			return nil, fmt.Errorf("acme TLS mode requires at least one domain")
		}
		cacheDir := m.cfg.ACME.CacheDir
		if cacheDir == "" {
			cacheDir = "/var/lib/monstack/certs"
		}
		if err := os.MkdirAll(cacheDir, 0700); err != nil {
			return nil, fmt.Errorf("failed to create ACME cache dir %s: %w", cacheDir, err)
		}
		m.autocert = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Email:      m.cfg.ACME.Email,
			HostPolicy: autocert.HostWhitelist(m.cfg.ACME.Domains...),
			Cache:      autocert.DirCache(cacheDir),
		}
		tlsCfg := m.autocert.TLSConfig()
		tlsCfg.MinVersion = tls.VersionTLS12
		return tlsCfg, nil

	case "self-signed":
		cert, err := generateSelfSignedCert()
		if err != nil {
			return nil, fmt.Errorf("failed to generate self-signed TLS certificate: %w", err)
		}
		return &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}, nil

	default:
		return nil, fmt.Errorf("unsupported TLS mode: %q", m.cfg.Mode)
	}
}

// AutocertManager returns the underlying autocert.Manager when running in ACME mode (or nil otherwise).
func (m *Manager) AutocertManager() *autocert.Manager {
	return m.autocert
}

func generateSelfSignedCert() (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, err
	}

	now := time.Now().UTC()
	tmpl := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"MonStack-Go"},
			CommonName:   "localhost",
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	return tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
	}, nil
}
