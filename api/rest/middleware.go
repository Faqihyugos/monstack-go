package rest

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/go-chi/cors"
)

// responseWriterInterceptor wraps http.ResponseWriter to capture the HTTP status code and response size.
type responseWriterInterceptor struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int64
}

func (w *responseWriterInterceptor) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriterInterceptor) Write(b []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += int64(n)
	return n, err
}

// Logging returns middleware that records structured request and response logs using slog.
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &responseWriterInterceptor{ResponseWriter: w}

			next.ServeHTTP(ww, r)

			duration := time.Since(start)
			status := ww.statusCode
			if status == 0 {
				status = http.StatusOK
			}

			logger.Info("http request completed",
				"method", r.Method,
				"path", r.URL.Path,
				"query", r.URL.RawQuery,
				"status", status,
				"bytes", ww.bytesWritten,
				"duration_ms", duration.Milliseconds(),
				"remote_addr", r.RemoteAddr,
				"user_agent", r.UserAgent(),
			)
		})
	}
}

// Recoverer returns middleware that catches panics and returns a 500 JSON response.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					stack := debug.Stack()
					logger.Error("panic recovered in HTTP handler",
						"error", fmt.Sprintf("%v", rec),
						"stack", string(stack),
						"method", r.Method,
						"path", r.URL.Path,
					)

					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]string{
						"error": "internal server error",
					})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// CORS returns configured CORS middleware.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	if len(allowedOrigins) == 0 {
		allowedOrigins = []string{"*"}
	}
	return cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	})
}

// Auth returns middleware enforcing authentication if enabled in config.
func Auth(cfg *config.AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If auth is disabled or unconfigured, allow all
			if cfg == nil || !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			// Public / exempt paths
			path := r.URL.Path
			if path == "/" || path == "/metrics" || path == "/api/v1/health" || strings.HasPrefix(path, "/static/") {
				next.ServeHTTP(w, r)
				return
			}

			// 1. Try Basic Auth
			user, pass, ok := r.BasicAuth()
			if ok {
				if validateBasicAuth(cfg, user, pass) {
					next.ServeHTTP(w, r)
					return
				}
			}

			// 2. Try Bearer Token
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				token := strings.TrimPrefix(authHeader, "Bearer ")
				if validateBearerToken(cfg, token) {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Unauthorized response
			w.Header().Set("WWW-Authenticate", `Basic realm="MonStack"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "unauthorized",
			})
		})
	}
}

func validateBasicAuth(cfg *config.AuthConfig, user, pass string) bool {
	if cfg.AdminUser == "" {
		return false
	}
	userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(cfg.AdminUser)) == 1
	if !userMatch {
		return false
	}

	if cfg.AdminPassword != "" {
		if subtle.ConstantTimeCompare([]byte(pass), []byte(cfg.AdminPassword)) == 1 {
			return true
		}
	}
	if cfg.AdminPasswordHash != "" {
		if subtle.ConstantTimeCompare([]byte(pass), []byte(cfg.AdminPasswordHash)) == 1 {
			return true
		}
	}
	return false
}

func validateBearerToken(cfg *config.AuthConfig, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}

	// Match admin password as bearer
	if cfg.AdminPassword != "" && subtle.ConstantTimeCompare([]byte(token), []byte(cfg.AdminPassword)) == 1 {
		return true
	}

	// Match JWT secret
	if cfg.JWT.Secret != "" && subtle.ConstantTimeCompare([]byte(token), []byte(cfg.JWT.Secret)) == 1 {
		return true
	}

	// Match API keys
	for _, ak := range cfg.APIKeys {
		if ak.Key != "" && subtle.ConstantTimeCompare([]byte(token), []byte(ak.Key)) == 1 {
			return true
		}
	}

	return false
}
