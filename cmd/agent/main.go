package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Faqihyugos/monstack-go/internal/agent"
)

var (
	version   = "v1.1.0-dev"
	buildTime = "unknown"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var (
		serverAddr string
		hostname   string
		agentID    string
		collectors string
		interval   time.Duration
		tlsEnabled bool
		skipVerify bool
	)

	rootCmd := &cobra.Command{
		Use:   "monstack-agent",
		Short: "MonStack-Go Lightweight Monitoring Agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			if serverAddr == "" {
				return fmt.Errorf("--server address is required (e.g. --server localhost:9443)")
			}

			var colList []string
			for _, c := range strings.Split(collectors, ",") {
				if trimmed := strings.TrimSpace(c); trimmed != "" {
					colList = append(colList, trimmed)
				}
			}

			log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
			ag := agent.New(agent.Options{
				ServerAddr:      serverAddr,
				Hostname:        hostname,
				AgentID:         agentID,
				Version:         version,
				TLSEnabled:      tlsEnabled,
				TLSSkipVerify:   skipVerify,
				CollectInterval: interval,
				Collectors:      colList,
				Logger:          log,
			})

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			log.Info("starting monstack-agent", "version", version, "server", serverAddr, "collectors", colList)
			if err := ag.Run(ctx); err != nil {
				return err
			}
			log.Info("monstack-agent stopped gracefully")
			return nil
		},
	}

	rootCmd.Flags().StringVar(&serverAddr, "server", "", "MonStack gRPC server address (host:port)")
	rootCmd.Flags().StringVar(&hostname, "hostname", "", "Override agent hostname")
	rootCmd.Flags().StringVar(&agentID, "id", "", "Optional fixed agent ID")
	rootCmd.Flags().StringVar(&collectors, "collectors", "cpu,memory,disk,network,load", "Comma-separated collectors (cpu,memory,disk,network,load,zfs,bird)")
	rootCmd.Flags().DurationVar(&interval, "interval", 10*time.Second, "Metric collection interval")
	rootCmd.Flags().BoolVar(&tlsEnabled, "tls", false, "Enable TLS for gRPC connection")
	rootCmd.Flags().BoolVar(&skipVerify, "tls-skip-verify", false, "Skip TLS certificate verification")

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print monstack-agent version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("monstack-agent version %s (built %s)\n", version, buildTime)
		},
	})

	return rootCmd
}
