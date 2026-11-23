package harness

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HTTPRequestRecord records details of an HTTP request received by MockHTTPServer.
type HTTPRequestRecord struct {
	Method    string
	Path      string
	RawQuery  string
	Query     url.Values
	Header    http.Header
	Body      []byte
	Timestamp time.Time
}

// TLSCertOptions configures self-signed TLS certificate generation.
type TLSCertOptions struct {
	CommonName   string
	DNSNames     []string
	IPAddresses  []net.IP
	ValidFor     time.Duration
	NotBefore    time.Time
	NotAfter     time.Time
	Organization string
}

// GenerateSelfSignedCert generates a self-signed X.509 certificate and private key.
func GenerateSelfSignedCert(opts TLSCertOptions) (tls.Certificate, *x509.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	cn := opts.CommonName
	if cn == "" {
		cn = "127.0.0.1"
	}

	notBefore := opts.NotBefore
	if notBefore.IsZero() {
		notBefore = time.Now().Add(-1 * time.Hour)
	}

	notAfter := opts.NotAfter
	if notAfter.IsZero() {
		validFor := opts.ValidFor
		if validFor == 0 {
			validFor = 365 * 24 * time.Hour
		}
		notAfter = notBefore.Add(validFor)
	}

	dnsNames := opts.DNSNames
	if len(dnsNames) == 0 {
		dnsNames = []string{"localhost", "127.0.0.1"}
	}

	ipAddresses := opts.IPAddresses
	if len(ipAddresses) == 0 {
		ipAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback}
	}

	org := opts.Organization
	if org == "" {
		org = "MonStack Test Harness"
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   cn,
			Organization: []string{org},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	parsedCert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("failed to parse generated certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("failed to parse tls key pair: %w", err)
	}

	return tlsCert, parsedCert, nil
}

// MockHTTPServer provides a configurable HTTP/HTTPS mock server for E2E prober tests.
type MockHTTPServer struct {
	server             *httptest.Server
	mu                 sync.RWMutex
	requests           []HTTPRequestRecord
	defaultStatusCode  int
	defaultBody        string
	defaultHeaders     map[string]string
	delay              time.Duration
	routes             map[string]http.HandlerFunc
	tlsCert            *x509.Certificate
	closed             bool
}

// NewMockHTTPServer creates and starts an HTTP mock server with 200 OK default response.
func NewMockHTTPServer() *MockHTTPServer {
	m := &MockHTTPServer{
		defaultStatusCode: http.StatusOK,
		defaultBody:       `{"status":"ok"}`,
		defaultHeaders: map[string]string{
			"Content-Type": "application/json",
		},
		routes: make(map[string]http.HandlerFunc),
	}

	handler := http.HandlerFunc(m.handleRequest)
	m.server = httptest.NewServer(handler)
	return m
}

// NewMockHTTPSServer creates and starts an HTTPS mock server with a custom TLS certificate.
func NewMockHTTPSServer(opts ...TLSCertOptions) (*MockHTTPServer, error) {
	certOpt := TLSCertOptions{
		ValidFor: 365 * 24 * time.Hour,
	}
	if len(opts) > 0 {
		certOpt = opts[0]
	}

	tlsCert, parsedCert, err := GenerateSelfSignedCert(certOpt)
	if err != nil {
		return nil, fmt.Errorf("failed to generate TLS cert for mock server: %w", err)
	}

	m := &MockHTTPServer{
		defaultStatusCode: http.StatusOK,
		defaultBody:       `{"status":"ok"}`,
		defaultHeaders: map[string]string{
			"Content-Type": "application/json",
		},
		routes:  make(map[string]http.HandlerFunc),
		tlsCert: parsedCert,
	}

	handler := http.HandlerFunc(m.handleRequest)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
	}
	server.StartTLS()
	m.server = server

	return m, nil
}

// handleRequest dispatches incoming HTTP requests to configured routes or default response.
func (m *MockHTTPServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	// Read and record body
	var bodyBytes []byte
	if r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	}

	rec := HTTPRequestRecord{
		Method:    r.Method,
		Path:      r.URL.Path,
		RawQuery:  r.URL.RawQuery,
		Query:     r.URL.Query(),
		Header:    r.Header.Clone(),
		Body:      bodyBytes,
		Timestamp: time.Now(),
	}

	m.mu.Lock()
	m.requests = append(m.requests, rec)
	delay := m.delay
	routeHandler, hasRoute := m.routes[r.URL.Path]
	defaultStatus := m.defaultStatusCode
	defaultBody := m.defaultBody
	defaultHeaders := make(map[string]string, len(m.defaultHeaders))
	for k, v := range m.defaultHeaders {
		defaultHeaders[k] = v
	}
	m.mu.Unlock()

	// Simulated latency
	if delay > 0 {
		time.Sleep(delay)
	}

	// Route dispatch
	if hasRoute {
		routeHandler(w, r)
		return
	}

	// Default response
	for k, v := range defaultHeaders {
		w.Header().Set(k, v)
	}
	w.WriteHeader(defaultStatus)
	w.Write([]byte(defaultBody))
}

// URL returns the mock server base URL (e.g. "http://127.0.0.1:port").
func (m *MockHTTPServer) URL() string {
	if m.server == nil {
		return ""
	}
	return m.server.URL
}

// Host returns the host and port string of the mock server.
func (m *MockHTTPServer) Host() string {
	u, err := url.Parse(m.URL())
	if err != nil {
		return ""
	}
	return u.Host
}

// Port returns the integer port of the mock server.
func (m *MockHTTPServer) Port() int {
	host := m.Host()
	_, portStr, err := net.SplitHostPort(host)
	if err != nil {
		return 0
	}
	port, _ := strconv.Atoi(portStr)
	return port
}

// Close shuts down the mock server.
func (m *MockHTTPServer) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && m.server != nil {
		m.server.Close()
		m.closed = true
	}
}

// Client returns an http.Client configured with the server's TLS config (accepts self-signed certs).
func (m *MockHTTPServer) Client() *http.Client {
	return m.server.Client()
}

// SetDelay configures an artificial latency injected before every response.
func (m *MockHTTPServer) SetDelay(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delay = d
}

// SetDefaultResponse configures default status code, body, and optional headers.
func (m *MockHTTPServer) SetDefaultResponse(code int, body string, headers ...map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultStatusCode = code
	m.defaultBody = body
	if len(headers) > 0 && headers[0] != nil {
		m.defaultHeaders = headers[0]
	}
}

// AddRoute registers a custom handler func for a specific path.
func (m *MockHTTPServer) AddRoute(path string, handler http.HandlerFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes[path] = handler
}

// AddStatusRoute sets a route that returns a specific HTTP status code and text body.
func (m *MockHTTPServer) AddStatusRoute(path string, statusCode int, body ...string) {
	respBody := http.StatusText(statusCode)
	if len(body) > 0 {
		respBody = body[0]
	}
	m.AddRoute(path, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
		w.Write([]byte(respBody))
	})
}

// AddContentMatchRoute sets a route returning text or payload with specific content type.
func (m *MockHTTPServer) AddContentMatchRoute(path string, body string, contentType ...string) {
	ct := "text/plain; charset=utf-8"
	if len(contentType) > 0 && contentType[0] != "" {
		ct = contentType[0]
	}
	m.AddRoute(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	})
}

// AddRedirect registers an endpoint that redirects to targetURL with statusCode (301, 302, 307, etc.).
func (m *MockHTTPServer) AddRedirect(fromPath string, targetURL string, statusCode int) {
	m.AddRoute(fromPath, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetURL, statusCode)
	})
}

// AddRedirectChain registers a chain of n redirects ending at finalPath.
func (m *MockHTTPServer) AddRedirectChain(initialPath string, hops int, finalPath string, finalBody string) {
	if hops <= 0 {
		m.AddContentMatchRoute(initialPath, finalBody)
		return
	}

	curr := initialPath
	for i := 1; i < hops; i++ {
		next := fmt.Sprintf("%s-hop-%d", strings.TrimSuffix(initialPath, "/"), i)
		m.AddRedirect(curr, next, http.StatusFound)
		curr = next
	}
	m.AddRedirect(curr, finalPath, http.StatusFound)
	m.AddContentMatchRoute(finalPath, finalBody)
}

// GetRequests returns a snapshot copy of all recorded HTTP requests.
func (m *MockHTTPServer) GetRequests() []HTTPRequestRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]HTTPRequestRecord, len(m.requests))
	copy(res, m.requests)
	return res
}

// RequestCount returns the total number of requests received.
func (m *MockHTTPServer) RequestCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.requests)
}

// LastRequest returns the most recent request or nil if none received.
func (m *MockHTTPServer) LastRequest() *HTTPRequestRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.requests) == 0 {
		return nil
	}
	rec := m.requests[len(m.requests)-1]
	return &rec
}

// ClearRequests clears the recorded request log.
func (m *MockHTTPServer) ClearRequests() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = nil
}

// TLSCertificate returns the parsed x509 Certificate if this is an HTTPS server.
func (m *MockHTTPServer) TLSCertificate() *x509.Certificate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tlsCert
}
