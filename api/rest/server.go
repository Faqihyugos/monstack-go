package rest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/internal/prober"
	"github.com/Faqihyugos/monstack-go/internal/storage"
	"github.com/Faqihyugos/monstack-go/pkg/health"
	"github.com/Faqihyugos/monstack-go/pkg/metrics"
	"github.com/Faqihyugos/monstack-go/pkg/models"
	"github.com/go-chi/chi/v5"
)

// Server wraps the REST API HTTP server and Chi router.
type Server struct {
	router     chi.Router
	handlers   *Handlers
	httpServer *http.Server
	listener   net.Listener
	tlsConfig  *tls.Config
	cfg        *config.Config
	logger     *slog.Logger
	mu         sync.Mutex
	running    bool
}

// ServerOption configures a Server instance.
type ServerOption func(*Server)

// NewServer initializes a new Server with dependencies and options.
func NewServer(opts ...ServerOption) *Server {
	s := &Server{
		logger: slog.Default(),
	}

	var handlerOpts []HandlersOption
	for _, opt := range opts {
		opt(s)
	}

	if s.cfg == nil {
		s.cfg = &config.Config{
			Server: config.ServerConfig{
				Listen:       ":8080",
				ReadTimeout:  10 * time.Second,
				WriteTimeout: 10 * time.Second,
			},
		}
	}

	// Prepare Handlers dependencies from Server fields
	if s.handlers == nil {
		s.handlers = NewHandlers(handlerOpts...)
	}

	var authCfg *config.AuthConfig
	if s.cfg != nil {
		authCfg = &s.cfg.Auth
	}

	s.router = NewRouter(s.handlers, authCfg, s.logger)
	return s
}

// WithServerMetaStore sets the MetadataStore for the server.
func WithServerMetaStore(meta storage.MetadataStore) ServerOption {
	return func(s *Server) {
		if s.handlers == nil {
			s.handlers = NewHandlers()
		}
		s.handlers.meta = meta
	}
}

// WithServerTSDB sets the TSDB for the server.
func WithServerTSDB(tsdb storage.TSDB) ServerOption {
	return func(s *Server) {
		if s.handlers == nil {
			s.handlers = NewHandlers()
		}
		s.handlers.tsdb = tsdb
	}
}

// WithServerEngine sets the Prober Engine for the server.
func WithServerEngine(engine prober.Engine) ServerOption {
	return func(s *Server) {
		if s.handlers == nil {
			s.handlers = NewHandlers()
		}
		s.handlers.prober = engine
	}
}

// WithServerConfig sets the Config for the server.
func WithServerConfig(cfg *config.Config) ServerOption {
	return func(s *Server) {
		s.cfg = cfg
		if s.handlers == nil {
			s.handlers = NewHandlers()
		}
		s.handlers.cfg = cfg
	}
}

// WithServerHealth sets the Health Registry for the server.
func WithServerHealth(hReg *health.Registry) ServerOption {
	return func(s *Server) {
		if s.handlers == nil {
			s.handlers = NewHandlers()
		}
		s.handlers.health = hReg
	}
}

// WithServerMetrics sets the Metrics collector for the server.
func WithServerMetrics(m *metrics.Metrics) ServerOption {
	return func(s *Server) {
		if s.handlers == nil {
			s.handlers = NewHandlers()
		}
		s.handlers.metrics = m
	}
}

// WithServerReloadFunc sets the configuration reload callback.
func WithServerReloadFunc(fn ReloadFunc) ServerOption {
	return func(s *Server) {
		if s.handlers == nil {
			s.handlers = NewHandlers()
		}
		s.handlers.reloadFunc = fn
	}
}

// WithServerLogger sets the structured logger for the server.
func WithServerLogger(logger *slog.Logger) ServerOption {
	return func(s *Server) {
		if logger != nil {
			s.logger = logger
			if s.handlers != nil {
				s.handlers.logger = logger
			}
		}
	}
}

// WithHandlers directly sets pre-configured Handlers on the server.
func WithHandlers(h *Handlers) ServerOption {
	return func(s *Server) {
		s.handlers = h
	}
}

// WithServerTLSConfig sets an optional TLS configuration for HTTPS serving.
func WithServerTLSConfig(tlsCfg *tls.Config) ServerOption {
	return func(s *Server) {
		s.tlsConfig = tlsCfg
	}
}

// WithServerTargetCallbacks registers callbacks triggered immediately on target create/update/delete.
func WithServerTargetCallbacks(onChange func(*models.Target), onDelete func(string)) ServerOption {
	return func(s *Server) {
		if s.handlers == nil {
			s.handlers = NewHandlers()
		}
		s.handlers.onTargetChange = onChange
		s.handlers.onTargetDelete = onDelete
	}
}

// Router returns the underlying Chi router.
func (s *Server) Router() chi.Router {
	return s.router
}

// Handlers returns the underlying Handlers instance.
func (s *Server) Handlers() *Handlers {
	return s.handlers
}

// ServeHTTP delegates request handling to the Chi router.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// Start begins listening on the configured address.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("server is already running")
	}

	addr := s.cfg.Server.Listen
	if addr == "" {
		addr = ":8080"
	}

	readTimeout := s.cfg.Server.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = 15 * time.Second
	}
	writeTimeout := s.cfg.Server.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = 15 * time.Second
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	s.listener = ln

	s.httpServer = &http.Server{
		Addr:         addr,
		Handler:      s.router,
		TLSConfig:    s.tlsConfig,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	}
	s.running = true
	s.mu.Unlock()

	s.logger.Info("REST API server listening", "address", ln.Addr().String(), "tls", s.tlsConfig != nil)

	if s.tlsConfig != nil {
		err = s.httpServer.ServeTLS(ln, "", "")
	} else {
		err = s.httpServer.Serve(ln)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop gracefully shuts down the HTTP server.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running || s.httpServer == nil {
		return nil
	}

	s.running = false
	s.logger.Info("stopping REST API server")
	return s.httpServer.Shutdown(ctx)
}

// Addr returns the network address the server is listening on, or empty if not yet started.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}
