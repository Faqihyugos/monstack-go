package collector

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/Faqihyugos/monstack-go/api/proto"
	"github.com/Faqihyugos/monstack-go/internal/storage"
	"github.com/Faqihyugos/monstack-go/pkg/metrics"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

type activeStream struct {
	mu     sync.Mutex
	stream proto.AgentService_ConnectServer
}

func (a *activeStream) Send(msg *proto.ServerMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stream.Send(msg)
}

// CollectorServer hosts the gRPC AgentService for agent registration, telemetry ingestion, and remote commands.
type CollectorServer struct {
	listenAddr    string
	version       string
	tlsConfig     *tls.Config
	maxRecvSize   int
	meta          storage.MetadataStore
	tsdb          storage.TSDB
	metrics       *metrics.Metrics
	logger        *slog.Logger
	heartbeatTTL  time.Duration

	grpcServer *grpc.Server
	listener   net.Listener

	mu      sync.RWMutex
	streams map[string]*activeStream
	running bool
	stopCh  chan struct{}
}

// NewCollectorServer constructs a new gRPC CollectorServer.
func NewCollectorServer(
	listenAddr string,
	version string,
	tlsCfg *tls.Config,
	meta storage.MetadataStore,
	tsdb storage.TSDB,
	m *metrics.Metrics,
	logger *slog.Logger,
) *CollectorServer {
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = metrics.Default()
	}
	if version == "" {
		version = "v1.0.0"
	}
	return &CollectorServer{
		listenAddr:   listenAddr,
		version:      version,
		tlsConfig:    tlsCfg,
		maxRecvSize:  16 * 1024 * 1024,
		meta:         meta,
		tsdb:         tsdb,
		metrics:      m,
		logger:       logger,
		heartbeatTTL: 45 * time.Second,
		streams:      make(map[string]*activeStream),
		stopCh:       make(chan struct{}),
	}
}

// Start begins listening for gRPC agent connections.
func (s *CollectorServer) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("collector gRPC server already running")
	}

	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("failed to listen gRPC on %s: %w", s.listenAddr, err)
	}
	s.listener = ln

	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(s.maxRecvSize),
	}
	if s.tlsConfig != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(s.tlsConfig)))
	}

	s.grpcServer = grpc.NewServer(opts...)
	proto.RegisterAgentServiceServer(s.grpcServer, s)
	s.running = true
	s.mu.Unlock()

	go s.livenessLoop()

	s.logger.Info("gRPC agent collector listening", "address", ln.Addr().String(), "tls", s.tlsConfig != nil)
	if err := s.grpcServer.Serve(ln); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}
	return nil
}

// Addr returns the bound listener address, or empty if not started.
func (s *CollectorServer) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// Stop gracefully stops the gRPC server.
func (s *CollectorServer) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stopCh)
	srv := s.grpcServer
	s.mu.Unlock()

	if srv != nil {
		srv.GracefulStop()
	}
	s.logger.Info("gRPC agent collector stopped")
}

// Register handles one-shot agent registration and persists metadata in SQLite.
func (s *CollectorServer) Register(ctx context.Context, req *proto.RegisterRequest) (*proto.RegisterResponse, error) {
	if req == nil || req.Hostname == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname is required")
	}

	agentID := req.AgentID
	if agentID == "" {
		agentID = generateAgentID()
	}
	ip := req.IPAddress
	if ip == "" {
		ip = "unknown"
	}

	now := time.Now().UTC()
	agent := &models.Agent{
		ID:         agentID,
		Hostname:   req.Hostname,
		IPAddress:  ip,
		OS:         req.OS,
		Arch:       req.Arch,
		Version:    req.AgentVersion,
		Collectors: req.Collectors,
		Labels:     req.Labels,
		LastSeenAt: now,
		Status:     models.AgentStatusOnline,
		CreatedAt:  now,
	}

	if s.meta != nil {
		if err := s.meta.UpsertAgent(ctx, agent); err != nil {
			s.logger.Error("failed to register agent", "hostname", req.Hostname, "err", err)
			return nil, status.Errorf(codes.Internal, "failed to persist agent: %v", err)
		}
	}

	s.logger.Info("agent registered", "agent_id", agentID, "hostname", req.Hostname, "os", req.OS, "arch", req.Arch)
	return &proto.RegisterResponse{
		AgentID:  agentID,
		Accepted: true,
		Message:  "registered",
	}, nil
}

// HealthCheck responds with server health and version.
func (s *CollectorServer) HealthCheck(ctx context.Context, req *proto.HealthCheckRequest) (*proto.HealthCheckResponse, error) {
	return &proto.HealthCheckResponse{
		Healthy:       true,
		ServerVersion: s.version,
	}, nil
}

// Connect handles bi-directional telemetry streaming and remote command delivery.
func (s *CollectorServer) Connect(stream proto.AgentService_ConnectServer) error {
	var agentID string
	as := &activeStream{stream: stream}

	defer func() {
		if agentID != "" {
			s.mu.Lock()
			if cur, ok := s.streams[agentID]; ok && cur == as {
				delete(s.streams, agentID)
			}
			s.mu.Unlock()
			s.logger.Info("agent stream disconnected", "agent_id", agentID)
		}
	}()

	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}

		if msg.AgentID != "" && agentID == "" {
			agentID = msg.AgentID
			s.mu.Lock()
			s.streams[agentID] = as
			s.mu.Unlock()
			s.logger.Info("agent stream connected", "agent_id", agentID)
		}

		now := time.Now().UTC()
		if agentID != "" && s.meta != nil {
			_ = s.meta.UpdateAgentHeartbeat(stream.Context(), agentID, now, models.AgentStatusOnline)
		}

		if msg.Metrics != nil && len(msg.Metrics.Metrics) > 0 && s.tsdb != nil {
			points := make([]models.MetricPoint, 0, len(msg.Metrics.Metrics))
			for _, m := range msg.Metrics.Metrics {
				ts := now
				if m.Timestamp > 0 {
					ts = time.Unix(m.Timestamp, 0).UTC()
				}
				points = append(points, models.MetricPoint{
					TargetID:  agentID,
					Metric:    m.Name,
					Value:     m.Value,
					Timestamp: ts,
					Labels:    m.Labels,
				})
			}
			if err := s.tsdb.WritePoints(points); err != nil {
				s.logger.Warn("failed to persist agent metrics batch", "agent_id", agentID, "err", err)
			} else if s.metrics != nil {
				s.metrics.TSDBSamplesWritten.Add(float64(len(points)))
			}
		}

		// Send lightweight ACK
		_ = as.Send(&proto.ServerMessage{
			Type:    "ack",
			Message: "ok",
		})
	}
}

// SendCommand pushes a remote command to a currently connected agent via its gRPC stream.
func (s *CollectorServer) SendCommand(agentID, command string, params map[string]string) error {
	s.mu.RLock()
	as, ok := s.streams[agentID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("agent %q is not currently connected", agentID)
	}
	return as.Send(&proto.ServerMessage{
		Type:    "command",
		Command: command,
		Params:  params,
	})
}

// ConnectedAgents returns the count of active agent gRPC streams.
func (s *CollectorServer) ConnectedAgents() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.streams)
}

func (s *CollectorServer) livenessLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			if s.meta == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			agents, err := s.meta.ListAgents(ctx, string(models.AgentStatusOnline))
			if err == nil {
				cutoff := time.Now().UTC().Add(-s.heartbeatTTL)
				for _, a := range agents {
					if a.LastSeenAt.Before(cutoff) {
						_ = s.meta.UpdateAgentHeartbeat(ctx, a.ID, a.LastSeenAt, models.AgentStatusOffline)
					}
				}
			}
			cancel()
		}
	}
}

func generateAgentID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("agent-%x", b[:8])
}
