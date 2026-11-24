package web_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Faqihyugos/monstack-go/web"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebHandler(t *testing.T) {
	handler := web.Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")

	body := rec.Body.String()
	assert.Contains(t, body, "<!DOCTYPE html>")
	assert.Contains(t, strings.ToLower(body), "monstack")
}

func TestSubFS(t *testing.T) {
	sub, err := web.SubFS("")
	require.NoError(t, err)

	f, err := sub.Open("index.html")
	require.NoError(t, err)
	defer f.Close()

	data, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Contains(t, string(data), "MonStack")
}
