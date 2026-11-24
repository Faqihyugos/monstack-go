package prober

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// GRPCProber checks standard gRPC Health Checking Protocol (grpc.health.v1.Health/Check).
type GRPCProber struct{}

// NewGRPCProber creates a new GRPCProber.
func NewGRPCProber() *GRPCProber {
	return &GRPCProber{}
}

// Probe invokes Health.Check on the target gRPC endpoint and validates SERVING status.
func (p *GRPCProber) Probe(ctx context.Context, target *models.Target) (*models.ProbeResult, error) {
	now := time.Now().UTC()
	res := &models.ProbeResult{
		TargetID:   target.ID,
		TargetName: target.Name,
		TargetType: target.Type,
		Timestamp:  now,
		Metadata:   make(map[string]any),
	}

	service := getConfigString(target.Config, "service")
	useTLS := false
	if v, ok := target.Config["tls"].(bool); ok {
		useTLS = v
	}
	skipVerify := false
	if v, ok := target.Config["skip_verify"].(bool); ok {
		skipVerify = v
	}

	var creds credentials.TransportCredentials
	if useTLS {
		creds = credentials.NewTLS(&tls.Config{
			InsecureSkipVerify: skipVerify,
			MinVersion:         tls.VersionTLS12,
		})
	} else {
		creds = insecure.NewCredentials()
	}

	start := time.Now()
	conn, err := grpc.NewClient(target.Target, grpc.WithTransportCredentials(creds))
	if err != nil {
		res.ResponseTime = time.Since(start)
		res.Success = false
		res.Error = fmt.Sprintf("grpc client init failed: %v", err)
		return res, nil
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: service})
	elapsed := time.Since(start)
	res.ResponseTime = elapsed

	res.Metrics = append(res.Metrics, models.MetricPoint{
		TargetID:  target.ID,
		Metric:    models.MetricGRPCResponseSeconds,
		Value:     elapsed.Seconds(),
		Timestamp: now,
	})

	if err != nil {
		st, _ := status.FromError(err)
		res.Metrics = append(res.Metrics, models.MetricPoint{
			TargetID:  target.ID,
			Metric:    models.MetricGRPCStatusCode,
			Value:     float64(st.Code()),
			Timestamp: now,
		})
		res.Metadata["grpc_code"] = st.Code().String()
		res.Success = false
		res.Error = fmt.Sprintf("grpc health check failed: %v", err)
		return res, nil
	}

	res.Metrics = append(res.Metrics, models.MetricPoint{
		TargetID:  target.ID,
		Metric:    models.MetricGRPCStatusCode,
		Value:     0, // codes.OK
		Timestamp: now,
	})
	res.Metadata["grpc_status"] = resp.GetStatus().String()
	res.Metadata["service"] = service

	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		res.Success = false
		res.Error = fmt.Sprintf("grpc service %q status is %s", service, resp.GetStatus().String())
		return res, nil
	}

	res.Success = true
	return res, nil
}
