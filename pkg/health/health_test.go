package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealth_EmptyRegistry(t *testing.T) {
	reg := NewRegistry("1.0.0")
	sh := reg.Check(context.Background())

	assert.Equal(t, StatusUP, sh.Status)
	assert.Equal(t, "1.0.0", sh.Version)
	assert.Empty(t, sh.Components)
	assert.NotEmpty(t, sh.Uptime)
	assert.GreaterOrEqual(t, sh.UptimeSeconds, float64(0))
}

func TestHealth_AllUP(t *testing.T) {
	reg := NewRegistry("1.0.0")

	err := reg.Register("db", func(ctx context.Context) ComponentStatus {
		return ComponentStatus{
			Status:    StatusUP,
			Details:   map[string]any{"connections": 10},
			CheckedAt: time.Now().UTC(),
		}
	})
	require.NoError(t, err)

	err = reg.Register("tsdb", func(ctx context.Context) ComponentStatus {
		return ComponentStatus{
			Status:    StatusUP,
			CheckedAt: time.Now().UTC(),
		}
	})
	require.NoError(t, err)

	sh := reg.Check(context.Background())
	assert.Equal(t, StatusUP, sh.Status)
	assert.Len(t, sh.Components, 2)
	assert.Equal(t, StatusUP, sh.Components["db"].Status)
	assert.Equal(t, StatusUP, sh.Components["tsdb"].Status)
}

func TestHealth_Degraded(t *testing.T) {
	reg := NewRegistry("1.0.0")

	_ = reg.Register("db", func(ctx context.Context) ComponentStatus {
		return ComponentStatus{Status: StatusUP}
	})
	_ = reg.Register("cache", func(ctx context.Context) ComponentStatus {
		return ComponentStatus{
			Status:  StatusDEGRADED,
			Details: map[string]any{"reason": "high memory"},
		}
	})

	sh := reg.Check(context.Background())
	assert.Equal(t, StatusDEGRADED, sh.Status)
}

func TestHealth_DownPrecedence(t *testing.T) {
	reg := NewRegistry("1.0.0")

	_ = reg.Register("db", func(ctx context.Context) ComponentStatus {
		return ComponentStatus{Status: StatusDOWN, Error: "connection timeout"}
	})
	_ = reg.Register("cache", func(ctx context.Context) ComponentStatus {
		return ComponentStatus{Status: StatusDEGRADED}
	})
	_ = reg.Register("web", func(ctx context.Context) ComponentStatus {
		return ComponentStatus{Status: StatusUP}
	})

	sh := reg.Check(context.Background())
	assert.Equal(t, StatusDOWN, sh.Status)
	assert.Equal(t, "connection timeout", sh.Components["db"].Error)
}

func TestHealth_PanicRecovery(t *testing.T) {
	reg := NewRegistry("1.0.0")

	_ = reg.Register("flaky_service", func(ctx context.Context) ComponentStatus {
		panic("nil pointer dereference inside driver")
	})

	sh := reg.Check(context.Background())
	assert.Equal(t, StatusDOWN, sh.Status)
	require.Contains(t, sh.Components, "flaky_service")
	assert.Equal(t, StatusDOWN, sh.Components["flaky_service"].Status)
	assert.Contains(t, sh.Components["flaky_service"].Error, "health check panic: nil pointer dereference inside driver")
}

func TestHealth_RegisterValidation(t *testing.T) {
	reg := NewRegistry("1.0.0")

	assert.Error(t, reg.Register("", func(ctx context.Context) ComponentStatus {
		return ComponentStatus{Status: StatusUP}
	}))

	assert.Error(t, reg.Register("db", nil))
}

func TestHealth_RegisterUnregister(t *testing.T) {
	reg := NewRegistry("1.0.0")

	checkFn := func(ctx context.Context) ComponentStatus {
		return ComponentStatus{Status: StatusUP}
	}

	require.NoError(t, reg.Register("temp", checkFn))
	assert.Len(t, reg.Check(context.Background()).Components, 1)

	reg.Unregister("temp")
	assert.Empty(t, reg.Check(context.Background()).Components)
}

func TestHealth_HTTPHandler(t *testing.T) {
	reg := NewRegistry("1.2.3")

	t.Run("Status UP returns 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		rec := httptest.NewRecorder()

		reg.HTTPHandler()(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var resp SystemHealth
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, StatusUP, resp.Status)
		assert.Equal(t, "1.2.3", resp.Version)
	})

	t.Run("Status DEGRADED returns 200", func(t *testing.T) {
		_ = reg.Register("cache", func(ctx context.Context) ComponentStatus {
			return ComponentStatus{Status: StatusDEGRADED}
		})
		defer reg.Unregister("cache")

		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		rec := httptest.NewRecorder()

		reg.HTTPHandler()(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp SystemHealth
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, StatusDEGRADED, resp.Status)
	})

	t.Run("Status DOWN returns 503", func(t *testing.T) {
		_ = reg.Register("db", func(ctx context.Context) ComponentStatus {
			return ComponentStatus{Status: StatusDOWN}
		})
		defer reg.Unregister("db")

		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		rec := httptest.NewRecorder()

		reg.HTTPHandler()(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

		var resp SystemHealth
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, StatusDOWN, resp.Status)
	})
}

func TestHealth_Concurrency(t *testing.T) {
	reg := NewRegistry("test")
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := "comp"
			_ = reg.Register(name, func(ctx context.Context) ComponentStatus {
				return ComponentStatus{Status: StatusUP}
			})
			_ = reg.Check(context.Background())
			reg.Unregister(name)
		}(i)
	}

	wg.Wait()
}

func TestDefaultRegistry(t *testing.T) {
	SetVersion("v2.0.0")

	err := Register("subsystem", func(ctx context.Context) ComponentStatus {
		return ComponentStatus{Status: StatusUP}
	})
	require.NoError(t, err)
	defer Unregister("subsystem")

	sh := Check(context.Background())
	assert.Equal(t, StatusUP, sh.Status)
	assert.Equal(t, "v2.0.0", sh.Version)
	assert.Contains(t, sh.Components, "subsystem")

	handler := Handler()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
