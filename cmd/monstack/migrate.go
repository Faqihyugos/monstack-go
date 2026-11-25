package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/Faqihyugos/monstack-go/internal/config"
)

type promConfig struct {
	ScrapeConfigs []promScrapeConfig `yaml:"scrape_configs"`
}

type promScrapeConfig struct {
	JobName        string              `yaml:"job_name"`
	MetricsPath    string              `yaml:"metrics_path"`
	ScrapeInterval string              `yaml:"scrape_interval"`
	ScrapeTimeout  string              `yaml:"scrape_timeout"`
	Params         map[string][]string `yaml:"params"`
	StaticConfigs  []promStaticConfig  `yaml:"static_configs"`
}

type promStaticConfig struct {
	Targets []string          `yaml:"targets"`
	Labels  map[string]string `yaml:"labels"`
}

func newMigrateCmd() *cobra.Command {
	var (
		fromProm string
		output   string
	)

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate scrape targets from original monstack / Prometheus prometheus.yml",
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromProm == "" {
				return fmt.Errorf("--from-prometheus path is required")
			}
			targets, err := MigratePrometheusFile(fromProm)
			if err != nil {
				return err
			}

			outDoc := map[string]any{
				"probes": map[string]any{
					"targets": targets,
				},
			}
			yamlBytes, err := yaml.Marshal(outDoc)
			if err != nil {
				return fmt.Errorf("failed to marshal migrated targets: %w", err)
			}

			if output == "" {
				fmt.Print(string(yamlBytes))
				return nil
			}
			if err := os.WriteFile(output, yamlBytes, 0644); err != nil {
				return fmt.Errorf("failed to write output file %s: %w", output, err)
			}
			fmt.Printf("Migrated %d targets to %s\n", len(targets), output)
			return nil
		},
	}

	cmd.Flags().StringVar(&fromProm, "from-prometheus", "", "Path to original prometheus.yml file")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output YAML file path (stdout if omitted)")
	return cmd
}

// MigratePrometheusFile converts Prometheus scrape_configs (including blackbox icmp/http and snmp jobs)
// into MonStack-Go ProbeTargetConfig entries.
func MigratePrometheusFile(path string) ([]config.ProbeTargetConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	return MigratePrometheusYAML(data)
}

// MigratePrometheusYAML converts raw prometheus.yml bytes into MonStack-Go ProbeTargetConfig entries.
func MigratePrometheusYAML(data []byte) ([]config.ProbeTargetConfig, error) {
	var pc promConfig
	if err := yaml.Unmarshal(data, &pc); err != nil {
		return nil, fmt.Errorf("failed to parse prometheus.yml: %w", err)
	}

	var result []config.ProbeTargetConfig
	seen := make(map[string]int)

	for _, job := range pc.ScrapeConfigs {
		jobLower := strings.ToLower(job.JobName)
		if jobLower == "prometheus" {
			continue
		}

		interval := 30 * time.Second
		if d, err := time.ParseDuration(job.ScrapeInterval); err == nil && d > 0 {
			interval = d
		}
		timeout := 10 * time.Second
		if d, err := time.ParseDuration(job.ScrapeTimeout); err == nil && d > 0 {
			timeout = d
		}
		if timeout > interval {
			timeout = interval
		}

		probeType := inferProbeType(job)

		for _, sc := range job.StaticConfigs {
			for _, rawTarget := range sc.Targets {
				baseName := sc.Labels["name"]
				if baseName == "" {
					baseName = sc.Labels["instance"]
				}
				if baseName == "" {
					baseName = fmt.Sprintf("%s-%s", job.JobName, sanitizeTargetName(rawTarget))
				}
				seen[baseName]++
				name := baseName
				if seen[baseName] > 1 {
					name = fmt.Sprintf("%s-%d", baseName, seen[baseName])
				}

				labels := make(map[string]string, len(sc.Labels)+1)
				for k, v := range sc.Labels {
					labels[k] = v
				}
				labels["migrated_from_job"] = job.JobName

				tgtAddr := rawTarget
				if probeType == "http" && !strings.HasPrefix(tgtAddr, "http://") && !strings.HasPrefix(tgtAddr, "https://") {
					tgtAddr = "http://" + tgtAddr + "/metrics"
				}

				tc := config.ProbeTargetConfig{
					Name:     name,
					Type:     probeType,
					Target:   tgtAddr,
					Interval: interval,
					Timeout:  timeout,
					Labels:   labels,
				}
				switch probeType {
				case "icmp":
					tc.ICMP = &config.ICMPProbeConfig{Count: 3, PacketSize: 64, Protocol: "ip4"}
				case "snmp":
					tc.SNMP = &config.SNMPProbeConfig{Version: "v2c", Port: 161, Community: "public"}
				case "http":
					tc.HTTP = &config.HTTPProbeConfig{Method: "GET", ExpectedStatus: []int{200}}
				}
				result = append(result, tc)
			}
		}
	}
	return result, nil
}

func inferProbeType(job promScrapeConfig) string {
	jobLower := strings.ToLower(job.JobName)
	mods := job.Params["module"]
	mod := ""
	if len(mods) > 0 {
		mod = strings.ToLower(mods[0])
	}

	switch {
	case strings.Contains(jobLower, "icmp") || mod == "icmp":
		return "icmp"
	case strings.Contains(jobLower, "snmp") || job.MetricsPath == "/snmp":
		return "snmp"
	case strings.HasPrefix(mod, "tcp"):
		return "tcp"
	default:
		return "http"
	}
}

func sanitizeTargetName(s string) string {
	r := strings.NewReplacer("://", "-", "/", "-", ":", "-", ".", "-")
	return strings.Trim(r.Replace(s), "-")
}
