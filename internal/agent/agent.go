package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Faqihyugos/monstack-go/api/proto"
	"github.com/Faqihyugos/monstack-go/internal/agent/collectors"
)

// Options configures a MonStack Agent instance.
type Options struct {
	ServerAddr      string
	Hostname        string
	AgentID         string
	Version         string
	TLSEnabled      bool
	TLSSkipVerify   bool
	CollectInterval time.Duration
	Collectors      []string
	Labels          map[string]string
	BIRDSocket      string
	MountPoints     []string
	Logger          *slog.Logger
	OnCommand       func(cmd string, params map[string]string)
}

// Agent collects local host metrics and streams them to a MonStack Server over gRPC.
type Agent struct {
	opts       Options
	agentID    string
	collectors []collectors.Collector
	logger     *slog.Logger
	startTime  time.Time

	mu     sync.Mutex
	conn   *grpc.ClientConn
	client proto.AgentServiceClient
}

// New creates a new Agent with configured collectors.
func New(opts Options) *Agent {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Hostname == "" {
		if h, err := os.Hostname(); err == nil {
			opts.Hostname = h
		} else {
			opts.Hostname = "unknown-host"
		}
	}
	if opts.Version == "" {
		opts.Version = "v1.0.0"
	}
	if opts.CollectInterval <= 0 {
		opts.CollectInterval = 10 * time.Second
	}
	if len(opts.Collectors) == 0 {
		opts.Collectors = []string{"cpu", "memory", "disk", "network", "load"}
	}

	var cols []collectors.Collector
	for _, name := range opts.Collectors {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "cpu":
			cols = append(cols, collectors.NewCPUCollector())
		case "memory":
			cols = append(cols, collectors.NewMemoryCollector())
		case "disk":
			cols = append(cols, collectors.NewDiskCollector(opts.MountPoints))
		case "network":
			cols = append(cols, collectors.NewNetworkCollector())
		case "load":
			cols = append(cols, collectors.NewLoadCollector())
		case "zfs":
			cols = append(cols, collectors.NewZFSCollector())
		case "bird":
			cols = append(cols, collectors.NewBIRDCollector(opts.BIRDSocket))
		}
	}

	return &Agent{
		opts:       opts,
		agentID:    opts.AgentID,
		collectors: cols,
		logger:     opts.Logger,
		startTime:  time.Now().UTC(),
	}
}

// AgentID returns the assigned agent identifier after registration.
func (a *Agent) AgentID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.agentID
}

// Run connects to the server, registers, and streams metrics until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	if a.opts.ServerAddr == "" {
		return errors.New("server address is required")
	}

	var creds credentials.TransportCredentials
	if a.opts.TLSEnabled {
		creds = credentials.NewTLS(&tls.Config{
			InsecureSkipVerify: a.opts.TLSSkipVerify,
			MinVersion:         tls.VersionTLS12,
		})
	} else {
		creds = insecure.NewCredentials()
	}

	conn, err := grpc.NewClient(a.opts.ServerAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("failed to create gRPC client: %w", err)
	}
	defer conn.Close()

	client := proto.NewAgentServiceClient(conn)
	a.mu.Lock()
	a.conn = conn
	a.client = client
	a.mu.Unlock()

	if err := a.register(ctx, client); err != nil {
		return err
	}

	return a.streamLoop(ctx, client)
}

func (a *Agent) register(ctx context.Context, client proto.AgentServiceClient) error {
	colNames := make([]string, 0, len(a.collectors))
	for _, c := range a.collectors {
		colNames = append(colNames, c.Name())
	}

	resp, err := client.Register(ctx, &proto.RegisterRequest{
		AgentID:      a.AgentID(),
		Hostname:     a.opts.Hostname,
		IPAddress:    detectLocalIP(),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		AgentVersion: a.opts.Version,
		Collectors:   colNames,
		Labels:       a.opts.Labels,
	})
	if err != nil {
		return fmt.Errorf("agent registration failed: %w", err)
	}
	if !resp.Accepted {
		return fmt.Errorf("agent registration rejected: %s", resp.Message)
	}

	a.mu.Lock()
	a.agentID = resp.AgentID
	a.mu.Unlock()

	a.logger.Info("agent registered with server", "agent_id", resp.AgentID, "server", a.opts.ServerAddr)
	return nil
}

func (a *Agent) streamLoop(ctx context.Context, client proto.AgentServiceClient) error {
	stream, err := client.Connect(ctx)
	if err != nil {
		return fmt.Errorf("failed to open Connect stream: %w", err)
	}

	// Receive server commands & acks in background
	recvErrCh := make(chan error, 1)
	go func() {
		for {
			srvMsg, err := stream.Recv()
			if err != nil {
				recvErrCh <- err
				return
			}
			if srvMsg.Type == "command" {
				a.logger.Info("received remote command from server", "command", srvMsg.Command, "params", srvMsg.Params)
				if a.opts.OnCommand != nil {
					a.opts.OnCommand(srvMsg.Command, srvMsg.Params)
				}
			}
		}
	}()

	// Send an initial metrics batch immediately
	if err := a.collectAndSend(ctx, stream); err != nil {
		return err
	}

	ticker := time.NewTicker(a.opts.CollectInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = stream.CloseSend()
			return nil
		case err := <-recvErrCh:
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("agent stream receive error: %w", err)
		case <-ticker.C:
			if err := a.collectAndSend(ctx, stream); err != nil {
				return err
			}
		}
	}
}

func (a *Agent) collectAndSend(ctx context.Context, stream proto.AgentService_ConnectClient) error {
	id := a.AgentID()
	var batch []proto.Metric

	for _, col := range a.collectors {
		pts, err := col.Collect(ctx, id)
		if err != nil {
			a.logger.Debug("collector failed", "collector", col.Name(), "err", err)
			continue
		}
		for _, pt := range pts {
			batch = append(batch, proto.Metric{
				Name:      pt.Metric,
				Value:     pt.Value,
				Timestamp: pt.Timestamp.Unix(),
				Labels:    pt.Labels,
			})
		}
	}

	msg := &proto.AgentMessage{
		AgentID:   id,
		Timestamp: time.Now().UTC().Unix(),
		Heartbeat: &proto.Heartbeat{
			Status:        "online",
			UptimeSeconds: time.Since(a.startTime).Seconds(),
		},
	}
	if len(batch) > 0 {
		msg.Metrics = &proto.MetricsBatch{Metrics: batch}
	}
	return stream.Send(msg)
}

func detectLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.String()
		}
	}
	return "127.0.0.1"
}
