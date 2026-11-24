package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/internal/server"
	"github.com/spf13/cobra"
)

var (
	version   = "v1.0.0-dev"
	buildTime = "unknown"
)

func main() {
	rootCmd := newRootCmd()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "monstack",
		Short: "MonStack-Go — Modern Infrastructure Monitoring Platform",
		Long:  "MonStack-Go is a lightweight, single-binary infrastructure monitoring platform with built-in HTTP/ICMP/TCP probers, SQLite+TSDB storage, REST API, embedded Web UI, and alerting.",
	}

	rootCmd.AddCommand(
		newServerCmd(),
		newVersionCmd(),
		newConfigCmd(),
	)

	return rootCmd
}

func newServerCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the MonStack-Go monitoring server",
		RunE: func(cmd *cobra.Command, args []string) error {
			srv, err := server.New(configPath, version)
			if err != nil {
				return fmt.Errorf("failed to initialize server: %w", err)
			}
			return srv.Run(context.Background())
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "path to configuration YAML file")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print MonStack-Go version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("monstack version %s (built %s)\n", version, buildTime)
		},
	}
}

func newConfigCmd() *cobra.Command {
	cfgCmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration utilities",
	}

	var configPath string
	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a MonStack-Go configuration file",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := config.Load(configPath); err != nil {
				return fmt.Errorf("configuration invalid: %w", err)
			}
			fmt.Println("Configuration is valid.")
			return nil
		},
	}
	validateCmd.Flags().StringVarP(&configPath, "config", "c", "", "path to configuration YAML file")
	cfgCmd.AddCommand(validateCmd)

	return cfgCmd
}
