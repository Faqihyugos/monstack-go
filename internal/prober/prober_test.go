package prober_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yourusername/monstack-go/internal/prober"
	"github.com/yourusername/monstack-go/pkg/metrics"
	"github.com/yourusername/monstack-go/pkg/models"
)

// mockTSDB implements storage.TSDB for engine persistence testing.
type mockTSDB struct {
	mu     sync.Mutex
	points []models.MetricPoint
}

func (m *mockTSDB) Close() error { return nil }
func (m *mockTSDB) WritePoint(p models.MetricPoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.points = append(m.points, p)
	return nil
}
func (m *mockTSDB) WritePoints(pts []models.MetricPoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.points = append(m.points, pts...)
	return nil
}
func (m *mockTSDB) QueryRange(targetID, metric string, start, end time.Time, step time.Duration) ([]models.MetricPoint, error) {
	return nil, nil
}
func (m *mockTSDB) GetLatestPoint(targetID, metric string) (*models.MetricPoint, error) {
	return nil, nil
}
func (m *mockTSDB) Prune(retention time.Duration) (int, error) {
	return 0, nil
}
func (m *mockTSDB) Points() []models.MetricPoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	pts := make([]models.MetricPoint, len(m.points))
	copy(pts, m.points)
	return pts
}

// generateCustomTLSServer creates an HTTPS test server with a certificate valid for a specific duration.
func generateCustomTLSServer(validFor time.Duration, handler http.Handler) (*httptest.Server, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serialNumber, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	notBefore := time.Now().Add(-1 * time.Hour)
	notAfter := notBefore.Add(validFor)

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "127.0.0.1",
			Organization: []string{"MonStack Test"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}

	cert := tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
	}

	ts := httptest.NewUnstartedServer(handler)
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	ts.StartTLS()
	return ts, nil
}

// -----------------------------------------------------------------------------
// Registry Tests
// -----------------------------------------------------------------------------

func TestRegistry(t *testing.T) {
	reg := prober.NewRegistry()

	// Initially empty
	assert.Empty(t, reg.List())
	p, ok := reg.Get(models.TargetTypeHTTP)
	assert.False(t, ok)
	assert.Nil(t, p)

	// Register HTTP and TCP
	httpP := prober.NewHTTPProber()
	tcpP := prober.NewTCPProber()
	reg.Register(models.TargetTypeHTTP, httpP)
	reg.Register(models.TargetTypeTCP, tcpP)

	assert.Len(t, reg.List(), 2)

	p, ok = reg.Get(models.TargetTypeHTTP)
	assert.True(t, ok)
	assert.Equal(t, httpP, p)

	p, ok = reg.Get(models.TargetTypeTCP)
	assert.True(t, ok)
	assert.Equal(t, tcpP, p)

	// Overwrite HTTP
	newHTTP := prober.NewHTTPProber()
	reg.Register(models.TargetTypeHTTP, newHTTP)
	p, ok = reg.Get(models.TargetTypeHTTP)
	assert.True(t, ok)
	assert.Equal(t, newHTTP, p)

	// Concurrent read/write race safety
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			reg.Register(models.TargetTypeICMP, prober.NewICMPProber())
		}()
		go func() {
			defer wg.Done()
			_, _ = reg.Get(models.TargetTypeICMP)
			_ = reg.List()
		}()
	}
	wg.Wait()
}

// -----------------------------------------------------------------------------
// Engine Tests
// -----------------------------------------------------------------------------

func TestEngine(t *testing.T) {
	t.Run("ExecuteNilTarget", func(t *testing.T) {
		eng := prober.NewDefaultEngine()
		res, err := eng.Execute(context.Background(), nil)
		require.Error(t, err)
		assert.Nil(t, res)
	})

	t.Run("ExecuteValidationFailure", func(t *testing.T) {
		eng := prober.NewDefaultEngine()
		target := &models.Target{
			ID:   "invalid-tg",
			Name: "", // Empty name fails validation
			Type: models.TargetTypeHTTP,
		}
		res, err := eng.Execute(context.Background(), target)
		require.Error(t, err)
		require.NotNil(t, res)
		assert.False(t, res.Success)
		assert.Contains(t, res.Error, "target validation failed")
	})

	t.Run("ExecuteUnsupportedProbeType", func(t *testing.T) {
		reg := prober.NewRegistry()
		eng := prober.NewEngine(reg)
		target := models.NewTarget("unsupported", models.TargetType("unknown_type"), "127.0.0.1", 1000, 500)
		res, err := eng.Execute(context.Background(), target)
		require.Error(t, err)
		require.NotNil(t, res)
		assert.False(t, res.Success)
		assert.Contains(t, res.Error, "invalid target type")
	})

	t.Run("ExecuteUnregisteredType", func(t *testing.T) {
		reg := prober.NewRegistry() // Empty registry
		eng := prober.NewEngine(reg)
		target := models.NewTarget("valid-unreg", models.TargetTypeTCP, "127.0.0.1:80", 1000, 500)
		target.ID = "tg-unreg-1"
		res, err := eng.Execute(context.Background(), target)
		require.Error(t, err)
		require.NotNil(t, res)
		assert.False(t, res.Success)
		assert.Contains(t, res.Error, "unsupported probe type")
	})

	t.Run("ExecuteSuccessAndCachingWithTSDB", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer ts.Close()

		tsdb := &mockTSDB{}
		customMetrics := metrics.NewMetrics()
		eng := prober.NewDefaultEngine(
			prober.WithTSDB(tsdb),
			prober.WithMetrics(customMetrics),
		)

		target := models.NewTarget("http-test", models.TargetTypeHTTP, ts.URL, 5000, 2000)
		target.ID = "tg-http-1"

		res, err := eng.Execute(context.Background(), target)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.True(t, res.Success)
		assert.Equal(t, "tg-http-1", res.TargetID)

		// Verify cached results
		cached, ok := eng.GetLastResult("tg-http-1")
		assert.True(t, ok)
		assert.Equal(t, res, cached)

		all := eng.GetAllLastResults()
		assert.Len(t, all, 1)
		assert.Equal(t, res, all["tg-http-1"])

		// Verify TSDB points
		points := tsdb.Points()
		require.NotEmpty(t, points)
		foundSuccess := false
		foundDuration := false
		for _, p := range points {
			if p.Metric == models.MetricProbeSuccess && p.Value == 1.0 {
				foundSuccess = true
			}
			if p.Metric == models.MetricProbeDurationSeconds && p.Value > 0 {
				foundDuration = true
			}
		}
		assert.True(t, foundSuccess, "probe_success metric point expected in TSDB")
		assert.True(t, foundDuration, "probe_duration_seconds metric point expected in TSDB")
	})

	t.Run("ExecuteConcurrencySafety", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		eng := prober.NewDefaultEngine()
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				tg := models.NewTarget(fmt.Sprintf("t-%d", idx), models.TargetTypeHTTP, ts.URL, 5000, 2000)
				tg.ID = fmt.Sprintf("tg-%d", idx)
				_, _ = eng.Execute(context.Background(), tg)
				_, _ = eng.GetLastResult(tg.ID)
				_ = eng.GetAllLastResults()
			}(i)
		}
		wg.Wait()
	})
}

// -----------------------------------------------------------------------------
// HTTP Prober Tests
// -----------------------------------------------------------------------------

func TestHTTPProber(t *testing.T) {
	p := prober.NewHTTPProber()

	t.Run("HTTP_200_OK_Success", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("healthy"))
		}))
		defer ts.Close()

		tg := models.NewTarget("ok-target", models.TargetTypeHTTP, ts.URL, 5000, 2000)
		tg.ID = "http-1"

		res, err := p.Probe(context.Background(), tg)
		require.NoError(t, err)
		assert.True(t, res.Success)
		assert.Empty(t, res.Error)
		assert.True(t, res.ResponseTime > 0)

		// Verify status code metric
		foundStatus := false
		for _, m := range res.Metrics {
			if m.Metric == models.MetricHTTPStatusCode {
				foundStatus = true
				assert.Equal(t, 200.0, m.Value)
			}
		}
		assert.True(t, foundStatus)
	})

	t.Run("HTTP_Non200_Failure", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("server crashed"))
		}))
		defer ts.Close()

		tg := models.NewTarget("err-target", models.TargetTypeHTTP, ts.URL, 5000, 2000)
		tg.ID = "http-2"

		res, err := p.Probe(context.Background(), tg)
		require.NoError(t, err)
		assert.False(t, res.Success)
		assert.Contains(t, res.Error, "status code 500 not in expected list")
	})

	t.Run("HTTP_CustomExpectedStatus", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated) // 201
		}))
		defer ts.Close()

		tg := models.NewTarget("created-target", models.TargetTypeHTTP, ts.URL, 5000, 2000)
		tg.ID = "http-3"
		tg.Config = map[string]any{
			"expected_status": []int{201, 204},
		}

		res, err := p.Probe(context.Background(), tg)
		require.NoError(t, err)
		assert.True(t, res.Success)
	})

	t.Run("HTTP_BodyMatch_SubstringAndRegex", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"app":"monstack","version":"1.2.3","status":"healthy"}`))
		}))
		defer ts.Close()

		// 1. Matching substring
		tgSuccess := models.NewTarget("bm-success", models.TargetTypeHTTP, ts.URL, 5000, 2000)
		tgSuccess.ID = "bm-1"
		tgSuccess.Config = map[string]any{"body_match": `"status":"healthy"`}

		res1, err := p.Probe(context.Background(), tgSuccess)
		require.NoError(t, err)
		assert.True(t, res1.Success)

		// 2. Matching regex
		tgRegex := models.NewTarget("bm-regex", models.TargetTypeHTTP, ts.URL, 5000, 2000)
		tgRegex.ID = "bm-2"
		tgRegex.Config = map[string]any{"body_match": `version":"1\.[0-9]+\.[0-9]+`}

		res2, err := p.Probe(context.Background(), tgRegex)
		require.NoError(t, err)
		assert.True(t, res2.Success)

		// 3. Mismatched pattern
		tgFail := models.NewTarget("bm-fail", models.TargetTypeHTTP, ts.URL, 5000, 2000)
		tgFail.ID = "bm-3"
		tgFail.Config = map[string]any{"body_match": `"status":"unhealthy"`}

		res3, err := p.Probe(context.Background(), tgFail)
		require.NoError(t, err)
		assert.False(t, res3.Success)
		assert.Contains(t, res3.Error, "body did not match expected pattern")
	})

	t.Run("HTTP_SSLCertificateExpiry_DaysRemaining", func(t *testing.T) {
		validDays := 45 * 24 * time.Hour
		ts, err := generateCustomTLSServer(validDays, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		require.NoError(t, err)
		defer ts.Close()

		tg := models.NewTarget("ssl-target", models.TargetTypeHTTP, ts.URL, 5000, 2000)
		tg.ID = "ssl-1"

		res, err := p.Probe(context.Background(), tg)
		require.NoError(t, err)
		assert.True(t, res.Success)

		foundSSL := false
		for _, m := range res.Metrics {
			if m.Metric == models.MetricHTTPSSLExpiryDays {
				foundSSL = true
				assert.InDelta(t, 45.0, m.Value, 2.0, "expected ~45 days remaining")
			}
		}
		assert.True(t, foundSSL, "probe_ssl_days_remaining metric expected")
	})

	t.Run("HTTP_RedirectFollowingAndLimit", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/hop1":
				http.Redirect(w, r, "/hop2", http.StatusFound)
			case "/hop2":
				http.Redirect(w, r, "/final", http.StatusFound)
			case "/final":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("at final destination"))
			case "/loop1":
				http.Redirect(w, r, "/loop2", http.StatusFound)
			case "/loop2":
				http.Redirect(w, r, "/loop1", http.StatusFound)
			}
		}))
		defer ts.Close()

		// 1. Follow redirects successfully
		tgSuccess := models.NewTarget("redir-ok", models.TargetTypeHTTP, ts.URL+"/hop1", 5000, 2000)
		tgSuccess.ID = "redir-1"
		tgSuccess.Config = map[string]any{
			"follow_redirects": true,
			"max_redirects":    5,
		}
		res1, err := p.Probe(context.Background(), tgSuccess)
		require.NoError(t, err)
		assert.True(t, res1.Success)
		assert.Equal(t, 2, res1.Metadata["redirect_count"])

		// 2. Exceed max redirects
		tgExceed := models.NewTarget("redir-exceed", models.TargetTypeHTTP, ts.URL+"/loop1", 5000, 2000)
		tgExceed.ID = "redir-2"
		tgExceed.Config = map[string]any{
			"follow_redirects": true,
			"max_redirects":    2,
		}
		res2, err := p.Probe(context.Background(), tgExceed)
		require.NoError(t, err)
		assert.False(t, res2.Success)
		assert.Contains(t, res2.Error, "stopped after 2 redirects")

		// 3. Do not follow redirects
		tgNoFollow := models.NewTarget("redir-nofollow", models.TargetTypeHTTP, ts.URL+"/hop1", 5000, 2000)
		tgNoFollow.ID = "redir-3"
		tgNoFollow.Config = map[string]any{
			"follow_redirects": false,
			"expected_status":  []int{302},
		}
		res3, err := p.Probe(context.Background(), tgNoFollow)
		require.NoError(t, err)
		assert.True(t, res3.Success)
		assert.Equal(t, 302, res3.Metadata["status_code"])
	})

	t.Run("HTTP_HeadersAndBasicAuth", func(t *testing.T) {
		var receivedAuth string
		var receivedHeader string

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedAuth = r.Header.Get("Authorization")
			receivedHeader = r.Header.Get("X-Custom-Header")
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		tg := models.NewTarget("auth-target", models.TargetTypeHTTP, ts.URL, 5000, 2000)
		tg.ID = "auth-1"
		tg.Config = map[string]any{
			"headers": map[string]string{
				"X-Custom-Header": "custom-val-123",
			},
			"basic_auth": map[string]any{
				"username": "monadmin",
				"password": "secretpassword",
			},
		}

		res, err := p.Probe(context.Background(), tg)
		require.NoError(t, err)
		assert.True(t, res.Success)
		assert.Equal(t, "custom-val-123", receivedHeader)
		assert.True(t, strings.HasPrefix(receivedAuth, "Basic "), "Authorization header expected")
	})

	t.Run("HTTP_InvalidTargetURL", func(t *testing.T) {
		tg := &models.Target{
			ID:     "invalid-url-tg",
			Name:   "bad-url",
			Type:   models.TargetTypeHTTP,
			Target: "://invalid",
		}
		res, err := p.Probe(context.Background(), tg)
		require.NoError(t, err)
		assert.False(t, res.Success)
		assert.Contains(t, res.Error, "invalid HTTP target URL")
	})
}

// -----------------------------------------------------------------------------
// ICMP Prober Tests
// -----------------------------------------------------------------------------

func TestICMPProber(t *testing.T) {
	p := prober.NewICMPProber()

	t.Run("ICMP_DisallowedURLScheme", func(t *testing.T) {
		tg := models.NewTarget("scheme-fail", models.TargetTypeICMP, "http://127.0.0.1", 1000, 500)
		res, err := p.Probe(context.Background(), tg)
		require.NoError(t, err)
		assert.False(t, res.Success)
		assert.Contains(t, res.Error, "must not have a URL scheme")

		foundLoss := false
		for _, m := range res.Metrics {
			if m.Metric == models.MetricICMPPacketLoss {
				foundLoss = true
				assert.Equal(t, 100.0, m.Value)
			}
		}
		assert.True(t, foundLoss)
	})

	t.Run("ICMP_ResolutionFailure", func(t *testing.T) {
		tg := models.NewTarget("dns-fail", models.TargetTypeICMP, "nonexistent-domain-xyz-987.invalid", 1000, 500)
		res, err := p.Probe(context.Background(), tg)
		require.NoError(t, err)
		assert.False(t, res.Success)
		assert.NotEmpty(t, res.Error)

		foundLoss := false
		for _, m := range res.Metrics {
			if m.Metric == models.MetricICMPPacketLoss {
				foundLoss = true
				assert.Equal(t, 100.0, m.Value)
			}
		}
		assert.True(t, foundLoss)
	})

	t.Run("ICMP_LoopbackPingReachability", func(t *testing.T) {
		tg := models.NewTarget("loopback", models.TargetTypeICMP, "127.0.0.1", 1000, 1000)
		tg.ID = "icmp-loopback-1"
		tg.Config = map[string]any{
			"count":    2,
			"protocol": "ip4",
		}

		res, err := p.Probe(context.Background(), tg)
		require.NoError(t, err)
		// On environments where UDP ping is permitted, success should be true
		// Verify that all required metrics are reported
		metricNames := make(map[string]float64)
		for _, m := range res.Metrics {
			metricNames[m.Metric] = m.Value
		}

		assert.Contains(t, metricNames, models.MetricICMPPacketLoss)
		assert.Contains(t, metricNames, models.MetricICMPRTTAverage)
		assert.Contains(t, metricNames, models.MetricICMPRTTMin)
		assert.Contains(t, metricNames, models.MetricICMPRTTMax)
		assert.Contains(t, metricNames, models.MetricICMPJitter)
	})

	t.Run("ICMP_UnreachableIPPacketLoss", func(t *testing.T) {
		// 192.0.2.1 is reserved documentation TEST-NET-1 (RFC 5737) and drops pings
		tg := models.NewTarget("unreachable", models.TargetTypeICMP, "192.0.2.1", 500, 200)
		tg.ID = "icmp-unreach-1"
		tg.Config = map[string]any{
			"count": 1,
		}

		res, err := p.Probe(context.Background(), tg)
		require.NoError(t, err)
		assert.False(t, res.Success)

		foundLoss := false
		for _, m := range res.Metrics {
			if m.Metric == models.MetricICMPPacketLoss {
				foundLoss = true
				assert.Equal(t, 100.0, m.Value)
			}
		}
		assert.True(t, foundLoss)
	})
}

// -----------------------------------------------------------------------------
// TCP Prober Tests
// -----------------------------------------------------------------------------

func TestTCPProber(t *testing.T) {
	p := prober.NewTCPProber()

	t.Run("TCP_OpenPort_Success", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()

		tg := models.NewTarget("tcp-open", models.TargetTypeTCP, ln.Addr().String(), 1000, 500)
		tg.ID = "tcp-1"

		res, err := p.Probe(context.Background(), tg)
		require.NoError(t, err)
		assert.True(t, res.Success)
		assert.Empty(t, res.Error)

		foundConnectMetric := false
		for _, m := range res.Metrics {
			if m.Metric == models.MetricTCPConnectSeconds {
				foundConnectMetric = true
				assert.True(t, m.Value > 0)
			}
		}
		assert.True(t, foundConnectMetric)
	})

	t.Run("TCP_ClosedPort_Rejection", func(t *testing.T) {
		// Get a free port and immediately close listener
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := ln.Addr().String()
		_ = ln.Close()

		tg := models.NewTarget("tcp-closed", models.TargetTypeTCP, addr, 1000, 500)
		tg.ID = "tcp-2"

		res, err := p.Probe(context.Background(), tg)
		require.NoError(t, err)
		assert.False(t, res.Success)
		assert.NotEmpty(t, res.Error)
	})

	t.Run("TCP_BannerGreetingMatch_SuccessAndFailure", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				_, _ = conn.Write([]byte("SSH-2.0-OpenSSH_9.0p1\r\n"))
				_ = conn.Close()
			}
		}()

		// 1. Matching banner
		tgSuccess := models.NewTarget("banner-ok", models.TargetTypeTCP, ln.Addr().String(), 1000, 500)
		tgSuccess.ID = "banner-1"
		tgSuccess.Config = map[string]any{"expect": "SSH-2.0"}

		res1, err := p.Probe(context.Background(), tgSuccess)
		require.NoError(t, err)
		assert.True(t, res1.Success)

		foundBannerMatch := false
		for _, m := range res1.Metrics {
			if m.Metric == models.MetricTCPBannerMatch {
				foundBannerMatch = true
				assert.Equal(t, 1.0, m.Value)
			}
		}
		assert.True(t, foundBannerMatch)

		// 2. Mismatched banner
		tgFail := models.NewTarget("banner-fail", models.TargetTypeTCP, ln.Addr().String(), 1000, 500)
		tgFail.ID = "banner-2"
		tgFail.Config = map[string]any{"expect": "MySQL-5.7"}

		res2, err := p.Probe(context.Background(), tgFail)
		require.NoError(t, err)
		assert.False(t, res2.Success)
		assert.Contains(t, res2.Error, "did not match expected")
	})

	t.Run("TCP_SendExpectPayloadValidation", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					buf := make([]byte, 1024)
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if string(buf[:n]) == "PING\r\n" {
						_, _ = c.Write([]byte("PONG\r\n"))
					}
				}(conn)
			}
		}()

		// 1. Success exchange
		tgSuccess := models.NewTarget("ping-pong-ok", models.TargetTypeTCP, ln.Addr().String(), 1000, 500)
		tgSuccess.ID = "pp-1"
		tgSuccess.Config = map[string]any{
			"send":   "PING\r\n",
			"expect": "PONG",
		}

		res1, err := p.Probe(context.Background(), tgSuccess)
		require.NoError(t, err)
		assert.True(t, res1.Success)

		// 2. Failure on expectation mismatch
		tgFail := models.NewTarget("ping-pong-fail", models.TargetTypeTCP, ln.Addr().String(), 1000, 500)
		tgFail.ID = "pp-2"
		tgFail.Config = map[string]any{
			"send":   "PING\r\n",
			"expect": "WRONG_RESPONSE",
		}

		res2, err := p.Probe(context.Background(), tgFail)
		require.NoError(t, err)
		assert.False(t, res2.Success)
	})
}

// -----------------------------------------------------------------------------
// Additional Edge Case & Coverage Tests
// -----------------------------------------------------------------------------

func TestEngine_RegisterAndGetProber(t *testing.T) {
	eng := prober.NewEngine(nil)
	assert.NotNil(t, eng)

	mockP := prober.NewHTTPProber()
	eng.RegisterProber(models.TargetTypeHTTP, mockP)

	retrieved, ok := eng.GetProber(models.TargetTypeHTTP)
	assert.True(t, ok)
	assert.Equal(t, mockP, retrieved)
}

func TestHTTPProber_WithClientAndNestedConfig(t *testing.T) {
	customClient := &http.Client{Timeout: 5 * time.Second}
	p := prober.NewHTTPProberWithClient(customClient)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "Bearer nested-token", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"nested":"ok"}`))
	}))
	defer ts.Close()

	tg := models.NewTarget("nested-http", models.TargetTypeHTTP, ts.URL, 5000, 2000)
	tg.ID = "nested-http-1"
	tg.Config = map[string]any{
		"http": map[string]any{
			"method":          "POST",
			"expected_status": []any{201.0, 200.0},
			"headers": map[string]any{
				"Authorization": "Bearer nested-token",
			},
			"body_match":       "nested",
			"follow_redirects": true,
			"max_redirects":    4.0,
			"tls": map[string]any{
				"skip_verify": true,
				"min_version": "1.2",
			},
		},
	}

	res, err := p.Probe(context.Background(), tg)
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Equal(t, 201, res.Metadata["status_code"])
}

func TestICMPProber_NestedConfig(t *testing.T) {
	p := prober.NewICMPProber()

	tg := models.NewTarget("nested-icmp", models.TargetTypeICMP, "127.0.0.1", 1000, 1000)
	tg.ID = "nested-icmp-1"
	tg.Config = map[string]any{
		"icmp": map[string]any{
			"count":       1.0,
			"packet_size": 32.0,
			"protocol":    "ip4",
		},
	}

	res, err := p.Probe(context.Background(), tg)
	require.NoError(t, err)
	assert.NotNil(t, res)
}

func TestTCPProber_AliasesAndNested(t *testing.T) {
	p := prober.NewTCPProber()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				n, _ := c.Read(buf)
				if string(buf[:n]) == "DATA\r\n" {
					_, _ = c.Write([]byte("ACK\r\n"))
				}
			}(conn)
		}
	}()

	// 1. Nested map with send_payload and expect_match
	tg := models.NewTarget("nested-tcp", models.TargetTypeTCP, ln.Addr().String(), 1000, 500)
	tg.ID = "nested-tcp-1"
	tg.Config = map[string]any{
		"tcp": map[string]any{
			"send_payload": "DATA\r\n",
			"expect_match": "ACK",
		},
	}

	res, err := p.Probe(context.Background(), tg)
	require.NoError(t, err)
	assert.True(t, res.Success)

	// 2. Top-level send without expect
	tgSendOnly := models.NewTarget("send-only-tcp", models.TargetTypeTCP, ln.Addr().String(), 1000, 500)
	tgSendOnly.ID = "send-only-1"
	tgSendOnly.Config = map[string]any{
		"send_payload": "DATA\r\n",
	}

	res2, err := p.Probe(context.Background(), tgSendOnly)
	require.NoError(t, err)
	assert.True(t, res2.Success)
}

func TestTCPProber_TLSConnection(t *testing.T) {
	// Start TLS server
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Extract host:port from ts.URL (stripping https://)
	addr := strings.TrimPrefix(ts.URL, "https://")

	p := prober.NewTCPProber()
	tg := models.NewTarget("tcp-tls", models.TargetTypeTCP, addr, 2000, 1000)
	tg.ID = "tcp-tls-1"
	tg.Config = map[string]any{
		"tls": true,
	}

	res, err := p.Probe(context.Background(), tg)
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.True(t, res.ResponseTime > 0)
}
