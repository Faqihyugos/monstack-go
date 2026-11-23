package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetrics_Isolation(t *testing.T) {
	// Creating multiple instances must not panic with duplicate metrics registration
	m1 := NewMetrics()
	m2 := NewMetrics()
	require.NotNil(t, m1)
	require.NotNil(t, m2)
	assert.NotEqual(t, m1.Registry(), m2.Registry())
}

func TestMetrics_RecordProbe(t *testing.T) {
	m := NewMetrics()

	m.RecordProbe("http", true, 150*time.Millisecond)
	m.RecordProbe("http", false, 5*time.Second)
	m.RecordProbe("icmp", true, 12*time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	assert.Contains(t, body, `monstack_probe_total{status="success",type="http"} 1`)
	assert.Contains(t, body, `monstack_probe_total{status="failure",type="http"} 1`)
	assert.Contains(t, body, `monstack_probe_total{status="success",type="icmp"} 1`)
	assert.Contains(t, body, `monstack_probe_duration_seconds_count{type="http"} 2`)
	assert.Contains(t, body, `monstack_probe_duration_seconds_count{type="icmp"} 1`)
}

func TestMetrics_RecordProbeError(t *testing.T) {
	m := NewMetrics()

	m.RecordProbeError("http", "timeout")
	m.RecordProbeError("http", "dns_lookup_failed")
	m.RecordProbeError("tcp", "connection_refused")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, `monstack_probe_errors_total{reason="timeout",type="http"} 1`)
	assert.Contains(t, body, `monstack_probe_errors_total{reason="dns_lookup_failed",type="http"} 1`)
	assert.Contains(t, body, `monstack_probe_errors_total{reason="connection_refused",type="tcp"} 1`)
}

func TestMetrics_SetTargetsCount(t *testing.T) {
	m := NewMetrics()

	m.SetTargetsCount("http", true, 10)
	m.SetTargetsCount("http", false, 2)
	m.SetTargetsCount("icmp", true, 5)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, `monstack_targets_total{enabled="true",type="http"} 10`)
	assert.Contains(t, body, `monstack_targets_total{enabled="false",type="http"} 2`)
	assert.Contains(t, body, `monstack_targets_total{enabled="true",type="icmp"} 5`)
}

func TestMetrics_RecordHTTPRequest(t *testing.T) {
	m := NewMetrics()

	m.RecordHTTPRequest("GET", "/api/v1/targets", 200, 5*time.Millisecond)
	m.RecordHTTPRequest("POST", "/api/v1/targets", 201, 12*time.Millisecond)
	m.RecordHTTPRequest("GET", "/api/v1/targets/99", 404, 2*time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, `monstack_http_requests_total{method="GET",path="/api/v1/targets",status="200"} 1`)
	assert.Contains(t, body, `monstack_http_requests_total{method="POST",path="/api/v1/targets",status="201"} 1`)
	assert.Contains(t, body, `monstack_http_requests_total{method="GET",path="/api/v1/targets/99",status="404"} 1`)
	assert.Contains(t, body, `monstack_http_request_duration_seconds_count{method="GET",path="/api/v1/targets"} 1`)
}

func TestMetrics_Alerts(t *testing.T) {
	m := NewMetrics()

	m.SetActiveAlerts("critical", 3)
	m.SetActiveAlerts("warning", 5)
	m.RecordAlertSent("telegram", "success")
	m.RecordAlertSent("webhook", "failed")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, `monstack_alerts_active{severity="critical"} 3`)
	assert.Contains(t, body, `monstack_alerts_active{severity="warning"} 5`)
	assert.Contains(t, body, `monstack_alerts_sent_total{channel="telegram",status="success"} 1`)
	assert.Contains(t, body, `monstack_alerts_sent_total{channel="webhook",status="failed"} 1`)
}

func TestMetrics_BuildInfo(t *testing.T) {
	m := NewMetrics()

	m.SetBuildInfo("1.0.0", "go1.23")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, `monstack_build_info{go_version="go1.23",version="1.0.0"} 1`)
}

func TestMetrics_Concurrency(t *testing.T) {
	m := NewMetrics()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			m.RecordProbe("http", id%2 == 0, time.Duration(id)*time.Millisecond)
			m.RecordProbeError("http", "err")
			m.SetTargetsCount("http", true, float64(id))
			m.RecordHTTPRequest("GET", "/test", 200, time.Millisecond)
			m.SetActiveAlerts("critical", float64(id%3))
			m.RecordAlertSent("telegram", "success")
		}(i)
	}

	wg.Wait()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestDefaultMetrics(t *testing.T) {
	RecordProbe("tcp", true, 10*time.Millisecond)
	RecordProbeError("tcp", "timeout")
	SetTargetsCount("tcp", true, 4)
	RecordHTTPRequest("GET", "/health", 200, time.Millisecond)
	SetActiveAlerts("info", 1)
	RecordAlertSent("email", "skipped")
	SetBuildInfo("dev", "go1.23")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, strings.Contains(rec.Body.String(), "monstack_probe_total"))
}
