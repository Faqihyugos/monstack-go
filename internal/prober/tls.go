package prober

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// TLSProber performs direct TLS handshakes and inspects certificate expiration and chain validity.
type TLSProber struct{}

// NewTLSProber creates a new TLSProber.
func NewTLSProber() *TLSProber {
	return &TLSProber{}
}

// Probe connects via TLS to target.Target (host:port or host defaulting to :443) and emits certificate metrics.
func (p *TLSProber) Probe(ctx context.Context, target *models.Target) (*models.ProbeResult, error) {
	now := time.Now().UTC()
	res := &models.ProbeResult{
		TargetID:   target.ID,
		TargetName: target.Name,
		TargetType: target.Type,
		Timestamp:  now,
		Metadata:   make(map[string]any),
	}

	timeout := 5 * time.Second
	if target.TimeoutMs > 0 {
		timeout = time.Duration(target.TimeoutMs) * time.Millisecond
	}

	addr := target.Target
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		addr = net.JoinHostPort(addr, "443")
	}

	serverName := getConfigString(target.Config, "server_name")
	if serverName == "" {
		serverName = host
	}
	skipVerify := false
	if v, ok := target.Config["skip_verify"].(bool); ok {
		skipVerify = v
	}

	minVer := uint16(tls.VersionTLS12)
	switch strings.TrimSpace(getConfigString(target.Config, "min_version")) {
	case "1.0":
		minVer = tls.VersionTLS10
	case "1.1":
		minVer = tls.VersionTLS11
	case "1.2":
		minVer = tls.VersionTLS12
	case "1.3":
		minVer = tls.VersionTLS13
	}

	tlsCfg := &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: skipVerify,
		MinVersion:         minVer,
	}

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: timeout},
		Config:    tlsCfg,
	}

	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	elapsed := time.Since(start)
	res.ResponseTime = elapsed
	res.Metrics = append(res.Metrics, models.MetricPoint{
		TargetID:  target.ID,
		Metric:    models.MetricHTTPTLSHandshakeSeconds,
		Value:     elapsed.Seconds(),
		Timestamp: now,
	})

	if err != nil {
		// Extract leaf certificate even on verification errors so expiry metrics remain visible
		var leaf *x509.Certificate
		var uaErr x509.UnknownAuthorityError
		var ciErr x509.CertificateInvalidError
		var hnErr x509.HostnameError
		if errors.As(err, &uaErr) && uaErr.Cert != nil {
			leaf = uaErr.Cert
		} else if errors.As(err, &ciErr) && ciErr.Cert != nil {
			leaf = ciErr.Cert
		} else if errors.As(err, &hnErr) && hnErr.Certificate != nil {
			leaf = hnErr.Certificate
		}
		if leaf != nil {
			recordCertMetrics(res, target.ID, leaf, false, now)
		}
		res.Success = false
		res.Error = fmt.Sprintf("tls handshake failed: %v", err)
		return res, nil
	}
	defer conn.Close()

	tlsConn := conn.(*tls.Conn)
	state := tlsConn.ConnectionState()
	res.Metadata["tls_version"] = tls.VersionName(state.Version)
	res.Metadata["cipher_suite"] = tls.CipherSuiteName(state.CipherSuite)

	if len(state.PeerCertificates) == 0 {
		res.Success = false
		res.Error = "no peer certificates presented"
		return res, nil
	}

	leaf := state.PeerCertificates[0]
	chainValid := !skipVerify || len(state.VerifiedChains) > 0
	recordCertMetrics(res, target.ID, leaf, chainValid, now)

	if time.Now().After(leaf.NotAfter) {
		res.Success = false
		res.Error = fmt.Sprintf("certificate expired at %s", leaf.NotAfter.UTC().Format(time.RFC3339))
		return res, nil
	}

	res.Success = true
	return res, nil
}

func recordCertMetrics(res *models.ProbeResult, targetID string, leaf *x509.Certificate, chainValid bool, now time.Time) {
	days := time.Until(leaf.NotAfter).Hours() / 24.0
	validVal := 0.0
	if chainValid {
		validVal = 1.0
	}
	res.Metrics = append(res.Metrics,
		models.MetricPoint{
			TargetID:  targetID,
			Metric:    models.MetricHTTPSSLExpiryDays,
			Value:     days,
			Timestamp: now,
		},
		models.MetricPoint{
			TargetID:  targetID,
			Metric:    models.MetricTLSChainValid,
			Value:     validVal,
			Timestamp: now,
		},
	)
	res.Metadata["ssl_days_remaining"] = days
	res.Metadata["issuer"] = leaf.Issuer.String()
	res.Metadata["subject"] = leaf.Subject.String()
	res.Metadata["not_after"] = leaf.NotAfter.UTC().Format(time.RFC3339)
	res.Metadata["dns_names"] = leaf.DNSNames
}
