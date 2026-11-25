package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Faqihyugos/monstack-go/api/rest"
	"github.com/Faqihyugos/monstack-go/internal/alerting"
	"github.com/Faqihyugos/monstack-go/internal/collector"
	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/internal/discovery"
	"github.com/Faqihyugos/monstack-go/internal/prober"
	"github.com/Faqihyugos/monstack-go/internal/scheduler"
	"github.com/Faqihyugos/monstack-go/internal/storage"
	"github.com/Faqihyugos/monstack-go/internal/storage/meta"
	"github.com/Faqihyugos/monstack-go/internal/storage/tsdb"
	tlsmanager "github.com/Faqihyugos/monstack-go/internal/tls"
	"github.com/Faqihyugos/monstack-go/pkg/health"
	"github.com/Faqihyugos/monstack-go/pkg/logger"
	"github.com/Faqihyugos/monstack-go/pkg/metrics"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// Server orchestrates all MonStack-Go subsystems.
type Server struct {
	configPath string
	version    string

	cfg       *config.Config
	watcher   *config.Watcher
	log       *slog.Logger
	metrics   *metrics.Metrics
	health    *health.Registry
	metaStore *meta.DB
	tsdbStore *tsdb.TSDB
	engine    *prober.DefaultEngine
	router    *alerting.Router
	silencer  *alerting.Silencer
	evaluator *alerting.Evaluator
	scheduler *scheduler.Scheduler
	discovery *discovery.Manager
	collector *collector.CollectorServer
	restSrv   *rest.Server

	mu      sync.Mutex
	stopped bool
}

// New creates and initializes a new MonStack-Go Server from the given config path.
func New(configPath, version string) (*Server, error) {
	if version == "" {
		version = "dev"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}

	log, err := logger.Init(logger.Config{
		Level:    cfg.Logging.Level,
		Format:   cfg.Logging.Format,
		Output:   cfg.Logging.Output,
		FilePath: cfg.Logging.File.Path,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}

	m := metrics.NewMetrics()
	m.SetBuildInfo(version, runtime.Version())

	hReg := health.NewRegistry(version)

	metaStore, err := meta.Open(cfg.Storage.Metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to open metadata store: %w", err)
	}

	tsdbStore, err := tsdb.Open(cfg.Storage.TSDB)
	if err != nil {
		_ = metaStore.Close()
		return nil, fmt.Errorf("failed to open tsdb store: %w", err)
	}

	engine := prober.NewDefaultEngine(
		prober.WithTSDB(tsdbStore),
		prober.WithMetrics(m),
	)

	alertRouter := alerting.NewRouter(cfg.Alerting, m, log)
	silencer := alerting.NewSilencer(metaStore, log)
	evaluator := alerting.NewEvaluator(
		metaStore,
		tsdbStore,
		engine,
		alertRouter,
		silencer,
		m,
		cfg.Alerting.EvaluationInterval,
		log,
	)

	sched := scheduler.NewScheduler(metaStore, engine, 16, m, log)
	sched.SetOnProbeComplete(evaluator.Trigger)

	discMgr := discovery.NewManager(cfg.Discovery, metaStore, func(t *models.Target) {
		sched.AddOrUpdateTarget(t)
	}, log)

	var grpcCollector *collector.CollectorServer
	if strings.TrimSpace(cfg.GRPC.Listen) != "" {
		grpcCollector = collector.NewCollectorServer(
			cfg.GRPC.Listen,
			version,
			nil,
			metaStore,
			tsdbStore,
			m,
			log,
		)
	}

	srv := &Server{
		configPath: configPath,
		version:    version,
		cfg:        cfg,
		log:        log,
		metrics:    m,
		health:     hReg,
		metaStore:  metaStore,
		tsdbStore:  tsdbStore,
		engine:     engine,
		router:     alertRouter,
		silencer:   silencer,
		evaluator:  evaluator,
		scheduler:  sched,
		discovery:  discMgr,
		collector:  grpcCollector,
	}

	// Register subsystem health checks
	_ = hReg.Register("storage", func(ctx context.Context) health.ComponentStatus {
		return health.ComponentStatus{Status: health.StatusUP, CheckedAt: time.Now().UTC()}
	})
	_ = hReg.Register("tsdb", func(ctx context.Context) health.ComponentStatus {
		return health.ComponentStatus{Status: health.StatusUP, CheckedAt: time.Now().UTC()}
	})
	_ = hReg.Register("prober", func(ctx context.Context) health.ComponentStatus {
		return health.ComponentStatus{Status: health.StatusUP, CheckedAt: time.Now().UTC()}
	})
	_ = hReg.Register("scheduler", func(ctx context.Context) health.ComponentStatus {
		return health.ComponentStatus{
			Status:    health.StatusUP,
			Details:   map[string]any{"active_targets": sched.ActiveCount()},
			CheckedAt: time.Now().UTC(),
		}
	})
	_ = hReg.Register("alerting", func(ctx context.Context) health.ComponentStatus {
		return health.ComponentStatus{Status: health.StatusUP, CheckedAt: time.Now().UTC()}
	})

	// Setup config watcher if a config file path was provided
	if configPath != "" {
		w, err := config.NewWatcher(configPath, cfg, log)
		if err != nil {
			log.Warn("failed to initialize config file watcher", "path", configPath, "err", err)
		} else {
			srv.watcher = w
			w.OnReload(srv.onConfigReload)
		}
	}

	restOpts := []rest.ServerOption{
		rest.WithServerMetaStore(metaStore),
		rest.WithServerTSDB(tsdbStore),
		rest.WithServerEngine(engine),
		rest.WithServerConfig(cfg),
		rest.WithServerHealth(hReg),
		rest.WithServerMetrics(m),
		rest.WithServerLogger(log),
		rest.WithServerTargetCallbacks(
			func(t *models.Target) { sched.AddOrUpdateTarget(t) },
			func(id string) { sched.RemoveTarget(id) },
		),
		rest.WithServerReloadFunc(func(ctx context.Context) error {
			return srv.Reload()
		}),
	}
	if grpcCollector != nil {
		restOpts = append(restOpts, rest.WithServerAgentCommander(grpcCollector.SendCommand))
	}

	// Configure TLS if server mode is https or auto
	mode := strings.ToLower(strings.TrimSpace(cfg.Server.Mode))
	if mode == "https" || mode == "auto" {
		tlsMgr := tlsmanager.NewManager(cfg.Server.TLS)
		tlsCfg, err := tlsMgr.TLSConfig()
		if err != nil {
			srv.cleanup()
			return nil, fmt.Errorf("failed to configure TLS: %w", err)
		}
		restOpts = append(restOpts, rest.WithServerTLSConfig(tlsCfg))
	}

	srv.restSrv = rest.NewServer(restOpts...)

	// Sync initial targets and alert rules from config into metadata store
	initCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.syncConfigTargets(initCtx, cfg)
	srv.syncConfigAlertRules(initCtx, cfg)

	return srv, nil
}

// Run starts all subsystems and blocks until a termination signal (SIGINT/SIGTERM) or context cancellation.
func (s *Server) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.log.Info("starting MonStack-Go server", "version", s.version, "listen", s.cfg.Server.Listen, "mode", s.cfg.Server.Mode)

	// 1. Start probe scheduler
	if err := s.scheduler.Start(runCtx); err != nil {
		return fmt.Errorf("failed to start scheduler: %w", err)
	}

	// 2. Start alert evaluator
	s.evaluator.Start(runCtx)

	// 2b. Start service discovery manager
	if s.discovery != nil {
		s.discovery.Start(runCtx)
	}

	// 3. Start TSDB retention pruner
	go s.retentionLoop(runCtx)

	// 3b. Start gRPC agent collector if configured
	if s.collector != nil {
		go func() {
			if err := s.collector.Start(runCtx); err != nil {
				s.log.Warn("gRPC agent collector stopped", "err", err)
			}
		}()
	}

	// 4. Start REST API & Web UI server
	errCh := make(chan error, 1)
	go func() {
		if err := s.restSrv.Start(runCtx); err != nil {
			errCh <- err
		}
		close(errCh)
	}()

	// 5. Listen for OS signals (SIGTERM, SIGINT, SIGHUP)
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	for {
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGHUP {
				s.log.Info("received SIGHUP signal, reloading configuration")
				if err := s.Reload(); err != nil {
					s.log.Error("SIGHUP configuration reload failed", "err", err)
				}
				continue
			}
			s.log.Info("received termination signal, initiating graceful shutdown", "signal", sig.String())
			cancel()
			return s.Shutdown()

		case <-ctx.Done():
			s.log.Info("context cancelled, initiating graceful shutdown")
			return s.Shutdown()

		case err := <-errCh:
			if err != nil {
				s.log.Error("REST server stopped with error", "err", err)
				_ = s.Shutdown()
				return err
			}
			return s.Shutdown()
		}
	}
}

// Reload reloads the configuration file from disk and applies updates to running subsystems.
func (s *Server) Reload() error {
	if s.watcher != nil {
		_, err := s.watcher.Reload()
		return err
	}
	if s.configPath == "" {
		return nil
	}
	newCfg, err := config.Load(s.configPath)
	if err != nil {
		return err
	}
	return s.onConfigReload(s.cfg, newCfg)
}

func (s *Server) onConfigReload(oldCfg, newCfg *config.Config) error {
	s.mu.Lock()
	s.cfg = newCfg
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s.restSrv.Handlers().SetConfig(newCfg)
	s.router.UpdateConfig(newCfg.Alerting)
	s.evaluator.SetInterval(newCfg.Alerting.EvaluationInterval)
	s.syncConfigTargets(ctx, newCfg)
	s.syncConfigAlertRules(ctx, newCfg)
	s.scheduler.SyncTargets(ctx)

	s.log.Info("configuration reload applied to all subsystems")
	return nil
}

// Shutdown gracefully stops all running subsystems.
func (s *Server) Shutdown() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	s.mu.Unlock()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var errs []error
	if s.restSrv != nil {
		if err := s.restSrv.Stop(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
	}
	if s.collector != nil {
		s.collector.Stop()
	}
	if s.discovery != nil {
		s.discovery.Stop()
	}
	if s.scheduler != nil {
		s.scheduler.Stop()
	}
	if s.evaluator != nil {
		s.evaluator.Stop()
	}
	s.cleanup()

	s.log.Info("graceful shutdown complete")
	return errors.Join(errs...)
}

func (s *Server) cleanup() {
	if s.watcher != nil {
		_ = s.watcher.Close()
	}
	if s.tsdbStore != nil {
		_ = s.tsdbStore.Close()
	}
	if s.metaStore != nil {
		_ = s.metaStore.Close()
	}
}

func (s *Server) retentionLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			retStr := s.cfg.Storage.TSDB.Retention
			s.mu.Unlock()
			ret, err := config.ParseDuration(retStr)
			if err == nil && ret > 0 {
				if pruned, err := s.tsdbStore.Prune(ret); err == nil && pruned > 0 {
					s.log.Info("pruned expired TSDB points", "count", pruned, "retention", retStr)
				}
			}
		}
	}
}

func (s *Server) syncConfigTargets(ctx context.Context, cfg *config.Config) {
	for _, tc := range cfg.Probes.Targets {
		intervalMs := int(tc.Interval.Milliseconds())
		if tc.IntervalMs > 0 {
			intervalMs = tc.IntervalMs
		}
		if intervalMs <= 0 {
			intervalMs = int(cfg.Probes.Defaults.Interval.Milliseconds())
		}
		if intervalMs <= 0 {
			intervalMs = 30000
		}

		timeoutMs := int(tc.Timeout.Milliseconds())
		if tc.TimeoutMs > 0 {
			timeoutMs = tc.TimeoutMs
		}
		if timeoutMs <= 0 {
			timeoutMs = int(cfg.Probes.Defaults.Timeout.Milliseconds())
		}
		if timeoutMs <= 0 {
			timeoutMs = 10000
		}
		if timeoutMs > intervalMs {
			timeoutMs = intervalMs
		}

		enabled := true
		if tc.Enabled != nil {
			enabled = *tc.Enabled
		}

		cfgMap := make(map[string]any)
		if tc.HTTP != nil {
			cfgMap["method"] = tc.HTTP.Method
			cfgMap["expected_status"] = tc.HTTP.ExpectedStatus
			cfgMap["headers"] = tc.HTTP.Headers
			cfgMap["body_match"] = tc.HTTP.BodyMatch
			cfgMap["follow_redirects"] = tc.HTTP.FollowRedirects
			cfgMap["max_redirects"] = tc.HTTP.MaxRedirects
			if tc.HTTP.TLS != nil {
				cfgMap["tls_skip_verify"] = tc.HTTP.TLS.SkipVerify
				cfgMap["tls_min_version"] = tc.HTTP.TLS.MinVersion
			}
		}
		if tc.ICMP != nil {
			cfgMap["count"] = tc.ICMP.Count
			cfgMap["packet_size"] = tc.ICMP.PacketSize
			cfgMap["protocol"] = tc.ICMP.Protocol
		}
		if tc.TCP != nil {
			cfgMap["send_payload"] = tc.TCP.Send
			cfgMap["expect_match"] = tc.TCP.Expect
			cfgMap["tls"] = tc.TCP.TLS
		}
		if tc.UDP != nil {
			cfgMap["send_payload"] = tc.UDP.Send
			cfgMap["expect_match"] = tc.UDP.Expect
		}
		if tc.DNS != nil {
			cfgMap["server"] = tc.DNS.Server
			cfgMap["record_type"] = tc.DNS.RecordType
			cfgMap["protocol"] = tc.DNS.Protocol
			cfgMap["expect_answer"] = tc.DNS.ExpectAnswer
		}
		if tc.SNMP != nil {
			cfgMap["version"] = tc.SNMP.Version
			cfgMap["port"] = tc.SNMP.Port
			cfgMap["community"] = tc.SNMP.Community
			cfgMap["oids"] = tc.SNMP.OIDs
			cfgMap["walk"] = tc.SNMP.Walk
		}
		if tc.GRPC != nil {
			cfgMap["service"] = tc.GRPC.Service
			cfgMap["tls"] = tc.GRPC.TLS
			cfgMap["skip_verify"] = tc.GRPC.SkipVerify
		}
		if tc.TLS != nil {
			cfgMap["server_name"] = tc.TLS.ServerName
			cfgMap["skip_verify"] = tc.TLS.SkipVerify
			cfgMap["min_version"] = tc.TLS.MinVersion
		}

		now := time.Now().UTC()
		tgt := &models.Target{
			ID:         tc.Name,
			Name:       tc.Name,
			Type:       models.TargetType(tc.Type),
			Target:     tc.Target,
			IntervalMs: intervalMs,
			TimeoutMs:  timeoutMs,
			Retries:    tc.Retries,
			Config:     cfgMap,
			Labels:     tc.Labels,
			Enabled:    enabled,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if tgt.Labels == nil {
			tgt.Labels = make(map[string]string)
		}

		existing, err := s.metaStore.GetTargetByName(ctx, tgt.Name)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				if err := s.metaStore.CreateTarget(ctx, tgt); err != nil {
					s.log.Warn("failed to seed target from config", "name", tgt.Name, "err", err)
				}
			}
			continue
		}
		tgt.ID = existing.ID
		tgt.CreatedAt = existing.CreatedAt
		if err := s.metaStore.UpdateTarget(ctx, tgt); err != nil {
			s.log.Warn("failed to update target from config", "name", tgt.Name, "err", err)
		}
	}
}

func (s *Server) syncConfigAlertRules(ctx context.Context, cfg *config.Config) {
	existingRules, err := s.metaStore.ListAlertRules(ctx, nil)
	if err != nil {
		s.log.Warn("failed to list existing alert rules", "err", err)
		return
	}
	byName := make(map[string]*models.AlertRule, len(existingRules))
	for _, r := range existingRules {
		byName[r.Name] = r
	}

	for _, rc := range cfg.Alerting.Rules {
		sev := models.AlertSeverity(strings.ToLower(strings.TrimSpace(rc.Severity)))
		if !sev.IsValid() {
			sev = models.SeverityWarning
		}
		now := time.Now().UTC()
		rule := &models.AlertRule{
			ID:          rc.Name,
			Name:        rc.Name,
			Expression:  rc.Expr,
			ForDuration: rc.For,
			Severity:    sev,
			Labels:      rc.Labels,
			Annotations: rc.Annotations,
			Channels:    rc.Channels,
			Enabled:     true,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if rule.Labels == nil {
			rule.Labels = make(map[string]string)
		}
		if rule.Annotations == nil {
			rule.Annotations = make(map[string]string)
		}

		if existing, ok := byName[rc.Name]; ok {
			rule.ID = existing.ID
			rule.CreatedAt = existing.CreatedAt
			if err := s.metaStore.UpdateAlertRule(ctx, rule); err != nil {
				s.log.Warn("failed to update alert rule from config", "name", rule.Name, "err", err)
			}
		} else {
			if err := s.metaStore.CreateAlertRule(ctx, rule); err != nil {
				s.log.Warn("failed to seed alert rule from config", "name", rule.Name, "err", err)
			}
		}
	}
}
