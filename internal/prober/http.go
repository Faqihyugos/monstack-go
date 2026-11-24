package prober

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/yourusername/monstack-go/pkg/models"
)

// HTTPProber executes HTTP and HTTPS endpoint probes.
type HTTPProber struct {
	client *http.Client
}

// NewHTTPProber creates an HTTPProber with default transport settings.
func NewHTTPProber() *HTTPProber {
	return &HTTPProber{}
}

// NewHTTPProberWithClient creates an HTTPProber using a custom HTTP client.
func NewHTTPProberWithClient(client *http.Client) *HTTPProber {
	return &HTTPProber{client: client}
}

// resolvedHTTPConfig holds the merged HTTP configuration.
type resolvedHTTPConfig struct {
	Method          string
	ExpectedStatus  []int
	Headers         map[string]string
	BodyMatch       string
	FollowRedirects bool
	MaxRedirects    int
	TLSSkipVerify   bool
	TLSMinVersion   string
}

// parseHTTPConfig extracts and merges HTTP configuration from target.
func parseHTTPConfig(target *models.Target) resolvedHTTPConfig {
	cfg := resolvedHTTPConfig{
		Method:          "GET",
		ExpectedStatus:  []int{200},
		Headers:         make(map[string]string),
		FollowRedirects: true,
		MaxRedirects:    3,
		TLSSkipVerify:   false,
	}

	if target.Config == nil || len(target.Config) == 0 {
		return cfg
	}

	// 1. Try target.GetHTTPConfig()
	if c, err := target.GetHTTPConfig(); err == nil && c != nil {
		if c.Method != "" {
			cfg.Method = c.Method
		}
		if len(c.ExpectedStatus) > 0 {
			cfg.ExpectedStatus = c.ExpectedStatus
		}
		if len(c.Headers) > 0 {
			for k, v := range c.Headers {
				cfg.Headers[k] = v
			}
		}
		if c.BodyMatch != "" {
			cfg.BodyMatch = c.BodyMatch
		}
		cfg.FollowRedirects = c.FollowRedirects
		if c.MaxRedirects > 0 {
			cfg.MaxRedirects = c.MaxRedirects
		}
		if c.TLSSkipVerify {
			cfg.TLSSkipVerify = true
		}
		if c.TLSMinVersion != "" {
			cfg.TLSMinVersion = c.TLSMinVersion
		}
	}

	// 2. Also check if nested "http" object exists in target.Config
	if httpRaw, ok := target.Config["http"]; ok {
		if httpMap, ok := httpRaw.(map[string]any); ok {
			if m, ok := httpMap["method"].(string); ok && m != "" {
				cfg.Method = m
			}
			if esRaw, ok := httpMap["expected_status"]; ok {
				if esSlice, ok := esRaw.([]any); ok {
					var statuses []int
					for _, s := range esSlice {
						if num, ok := s.(float64); ok {
							statuses = append(statuses, int(num))
						} else if num, ok := s.(int); ok {
							statuses = append(statuses, num)
						}
					}
					if len(statuses) > 0 {
						cfg.ExpectedStatus = statuses
					}
				} else if esInts, ok := esRaw.([]int); ok && len(esInts) > 0 {
					cfg.ExpectedStatus = esInts
				}
			}
			if hdrs, ok := httpMap["headers"].(map[string]any); ok {
				for k, v := range hdrs {
					if strVal, ok := v.(string); ok {
						cfg.Headers[k] = strVal
					}
				}
			}
			if bm, ok := httpMap["body_match"].(string); ok && bm != "" {
				cfg.BodyMatch = bm
			}
			if fr, ok := httpMap["follow_redirects"].(bool); ok {
				cfg.FollowRedirects = fr
			}
			if mr, ok := httpMap["max_redirects"].(float64); ok && mr > 0 {
				cfg.MaxRedirects = int(mr)
			} else if mr, ok := httpMap["max_redirects"].(int); ok && mr > 0 {
				cfg.MaxRedirects = mr
			}
			if tlsRaw, ok := httpMap["tls"].(map[string]any); ok {
				if sv, ok := tlsRaw["skip_verify"].(bool); ok {
					cfg.TLSSkipVerify = sv
				}
				if mv, ok := tlsRaw["min_version"].(string); ok {
					cfg.TLSMinVersion = mv
				}
			}
		}
	}

	// 3. Fallback check for body_match or body_match at top-level config
	if bm, ok := target.Config["body_match"].(string); ok && bm != "" {
		cfg.BodyMatch = bm
	}

	return cfg
}

// Probe executes an HTTP request according to target configuration, measuring
// latency, status code, SSL expiration, DNS lookup, TLS handshake, and body matching.
func (p *HTTPProber) Probe(ctx context.Context, target *models.Target) (*models.ProbeResult, error) {
	cfg := parseHTTPConfig(target)

	now := time.Now().UTC()
	result := &models.ProbeResult{
		TargetID:   target.ID,
		TargetName: target.Name,
		TargetType: target.Type,
		Timestamp:  now,
		Metadata:   make(map[string]any),
	}

	targetURL := target.Target
	parsedURL, err := url.ParseRequestURI(targetURL)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("invalid HTTP target URL: %v", err)
		return result, nil
	}

	// Prepare TLS configuration
	tlsConfig := &tls.Config{
		// For prober checks, allow InsecureSkipVerify so self-signed certs and internal
		// endpoints can be inspected for expiry and connectivity without failing root validation
		InsecureSkipVerify: true,
	}
	switch cfg.TLSMinVersion {
	case "1.3":
		tlsConfig.MinVersion = tls.VersionTLS13
	case "1.2":
		tlsConfig.MinVersion = tls.VersionTLS12
	case "1.1":
		tlsConfig.MinVersion = tls.VersionTLS11
	case "1.0":
		tlsConfig.MinVersion = tls.VersionTLS10
	}

	timeout := 10 * time.Second
	if target.TimeoutMs > 0 {
		timeout = time.Duration(target.TimeoutMs) * time.Millisecond
	}

	var redirectCount int
	var capturedPeerCerts []*x509.Certificate
	var dnsStart, dnsDone time.Time
	var tlsStart, tlsDone time.Time

	transport := &http.Transport{
		TLSClientConfig:       tlsConfig,
		DisableKeepAlives:     true,
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: timeout,
		TLSHandshakeTimeout:   timeout,
		DialContext: (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	client := p.client
	if client == nil {
		client = &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				redirectCount = len(via)
				if !cfg.FollowRedirects {
					return http.ErrUseLastResponse
				}
				maxRedir := cfg.MaxRedirects
				if maxRedir <= 0 {
					maxRedir = 10
				}
				if len(via) >= maxRedir {
					return fmt.Errorf("stopped after %d redirects (max %d)", len(via), maxRedir)
				}
				return nil
			},
		}
	}

	// Setup HTTP request
	req, err := http.NewRequestWithContext(ctx, cfg.Method, targetURL, nil)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("failed to create request: %v", err)
		return result, nil
	}

	// Apply basic auth from URL credentials if present
	if parsedURL.User != nil {
		pwd, _ := parsedURL.User.Password()
		req.SetBasicAuth(parsedURL.User.Username(), pwd)
	}

	// Check if basic auth is explicitly configured in target.Config
	if basicAuth, ok := target.Config["basic_auth"].(map[string]any); ok {
		u, _ := basicAuth["username"].(string)
		p, _ := basicAuth["password"].(string)
		if u != "" {
			req.SetBasicAuth(u, p)
		}
	}

	// Apply configured headers
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	// Setup trace for DNS and TLS measurements
	trace := &httptrace.ClientTrace{
		DNSStart: func(info httptrace.DNSStartInfo) {
			dnsStart = time.Now()
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			dnsDone = time.Now()
		},
		TLSHandshakeStart: func() {
			tlsStart = time.Now()
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			tlsDone = time.Now()
			if len(state.PeerCertificates) > 0 {
				capturedPeerCerts = state.PeerCertificates
			}
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	// Execute HTTP request
	startTime := time.Now()
	resp, doErr := client.Do(req)
	duration := time.Since(startTime)
	result.ResponseTime = duration

	// Record DNS latency metric if captured
	if !dnsStart.IsZero() && !dnsDone.IsZero() {
		dnsDuration := dnsDone.Sub(dnsStart)
		result.Metrics = append(result.Metrics, models.MetricPoint{
			TargetID:  target.ID,
			Metric:    models.MetricHTTPDNSLookupSeconds,
			Value:     dnsDuration.Seconds(),
			Timestamp: now,
		})
	}

	// Record TLS handshake latency metric if captured
	if !tlsStart.IsZero() && !tlsDone.IsZero() {
		tlsDuration := tlsDone.Sub(tlsStart)
		result.Metrics = append(result.Metrics, models.MetricPoint{
			TargetID:  target.ID,
			Metric:    models.MetricHTTPTLSHandshakeSeconds,
			Value:     tlsDuration.Seconds(),
			Timestamp: now,
		})
	}

	result.Metadata["redirect_count"] = redirectCount

	if doErr != nil {
		result.Success = false
		result.Error = doErr.Error()
		return result, nil
	}
	defer resp.Body.Close()

	// Record HTTP status code metric
	result.Metrics = append(result.Metrics, models.MetricPoint{
		TargetID:  target.ID,
		Metric:    models.MetricHTTPStatusCode,
		Value:     float64(resp.StatusCode),
		Timestamp: now,
	})
	result.Metadata["status_code"] = resp.StatusCode

	// Inspect TLS certificates for SSL expiration
	var certs []*x509.Certificate
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		certs = resp.TLS.PeerCertificates
	} else if len(capturedPeerCerts) > 0 {
		certs = capturedPeerCerts
	}
	if len(certs) > 0 {
		leaf := certs[0]
		remainingDays := time.Until(leaf.NotAfter).Hours() / 24.0
		result.Metrics = append(result.Metrics, models.MetricPoint{
			TargetID:  target.ID,
			Metric:    models.MetricHTTPSSLExpiryDays,
			Value:     remainingDays,
			Timestamp: now,
		})
		result.Metadata["ssl_days_remaining"] = remainingDays
		result.Metadata["ssl_issuer"] = leaf.Issuer.String()
		result.Metadata["ssl_subject"] = leaf.Subject.String()
		result.Metadata["ssl_not_after"] = leaf.NotAfter.UTC().Format(time.RFC3339)
	}

	// Read response body (capped at 5MB)
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	bodyStr := string(bodyBytes)

	// Validate expected status code
	statusOk := false
	for _, expected := range cfg.ExpectedStatus {
		if resp.StatusCode == expected {
			statusOk = true
			break
		}
	}

	// Validate content pattern match if specified
	contentMatchOk := true
	if cfg.BodyMatch != "" {
		matched := strings.Contains(bodyStr, cfg.BodyMatch)
		if !matched {
			if re, err := regexp.Compile(cfg.BodyMatch); err == nil && re.MatchString(bodyStr) {
				matched = true
			}
		}
		if matched {
			result.Metrics = append(result.Metrics, models.MetricPoint{
				TargetID:  target.ID,
				Metric:    models.MetricHTTPContentMatch,
				Value:     1.0,
				Timestamp: now,
			})
		} else {
			contentMatchOk = false
			result.Metrics = append(result.Metrics, models.MetricPoint{
				TargetID:  target.ID,
				Metric:    models.MetricHTTPContentMatch,
				Value:     0.0,
				Timestamp: now,
			})
		}
	}

	// Determine overall probe success
	result.Success = statusOk && contentMatchOk
	if !statusOk {
		expectedJSON, _ := json.Marshal(cfg.ExpectedStatus)
		result.Error = fmt.Sprintf("status code %d not in expected list %s", resp.StatusCode, string(expectedJSON))
	}
	if !contentMatchOk {
		if result.Error != "" {
			result.Error += "; "
		}
		result.Error += fmt.Sprintf("body did not match expected pattern: %s", cfg.BodyMatch)
	}

	return result, nil
}
