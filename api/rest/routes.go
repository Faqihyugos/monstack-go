package rest

import (
	"log/slog"

	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/go-chi/chi/v5"
)

// NewRouter constructs and configures the complete Chi v5 HTTP router.
func NewRouter(h *Handlers, authCfg *config.AuthConfig, logger *slog.Logger) chi.Router {
	if logger == nil {
		logger = slog.Default()
	}

	r := chi.NewRouter()

	// Global Middleware
	r.Use(Recoverer(logger))
	r.Use(Logging(logger))
	r.Use(CORS([]string{"*"}))
	r.Use(Auth(authCfg))

	// Root Web Dashboard & Prometheus Metrics
	r.Get("/", h.DashboardHandler)
	r.Get("/metrics", h.MetricsHandler)

	// API v1 Sub-router
	r.Route("/api/v1", func(r chi.Router) {
		// Health & System
		r.Get("/health", h.HealthHandler)
		r.Get("/config", h.GetConfigHandler)
		r.Put("/config/reload", h.ReloadConfigHandler)
		r.Post("/config/reload", h.ReloadConfigHandler)

		// Targets Management
		r.Route("/targets", func(r chi.Router) {
			r.Get("/", h.ListTargetsHandler)
			r.Post("/", h.CreateTargetHandler)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", h.GetTargetHandler)
				r.Put("/", h.UpdateTargetHandler)
				r.Delete("/", h.DeleteTargetHandler)
				r.Get("/status", h.GetTargetStatusHandler)
				r.Get("/metrics", h.GetTargetMetricsHandler)
			})
		})

		// Alerting & Rules
		r.Route("/alerts", func(r chi.Router) {
			r.Get("/", h.ListAlertsHandler)
			r.Route("/rules", func(r chi.Router) {
				r.Get("/", h.ListAlertRulesHandler)
				r.Post("/", h.CreateAlertRuleHandler)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetAlertRuleHandler)
					r.Put("/", h.UpdateAlertRuleHandler)
					r.Delete("/", h.DeleteAlertRuleHandler)
				})
			})
			r.Post("/silence", h.CreateSilenceHandler)
			r.Route("/silences", func(r chi.Router) {
				r.Get("/", h.ListSilencesHandler)
				r.Delete("/{id}", h.DeleteSilenceHandler)
			})
		})
	})

	return r
}
