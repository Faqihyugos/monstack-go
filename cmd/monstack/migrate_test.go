package main

import (
	"testing"
)

func TestMigratePrometheusYAML(t *testing.T) {
	// Representative prometheus.yml from original pndhkm/monstack
	promYAML := `
global:
  scrape_interval: 2s

scrape_configs:
  - job_name: 'prometheus'
    static_configs:
      - targets: ['localhost:9090']

  - job_name: 'snmp'
    metrics_path: /snmp
    scrape_interval: 60s
    scrape_timeout: 60s
    static_configs:
      - targets: ['192.168.77.2']
        labels:
          name: 'SW1'
    params:
      module: [if_mib]

  - job_name: icmp
    metrics_path: /probe
    static_configs:
      - targets: ['8.8.8.8']
        labels:
          name: 'Google'
      - targets: ['192.168.1.11']
        labels:
          name: 'My Website'
    params:
      module: [icmp]

  - job_name: zfs-exporter
    static_configs:
      - targets: ['192.168.1.12:9134']
        labels:
          instance: 'My ZFS Pool'
`
	targets, err := MigratePrometheusYAML([]byte(promYAML))
	if err != nil {
		t.Fatalf("MigratePrometheusYAML: %v", err)
	}
	if len(targets) != 4 {
		t.Fatalf("expected 4 migrated targets (SW1, Google, My Website, My ZFS Pool), got %d", len(targets))
	}
	if targets[0].Type != "snmp" || targets[0].Name != "SW1" {
		t.Errorf("expected first target to be snmp/SW1, got %s/%s", targets[0].Type, targets[0].Name)
	}
	if targets[1].Type != "icmp" || targets[1].Target != "8.8.8.8" {
		t.Errorf("expected second target to be icmp/8.8.8.8, got %s/%s", targets[1].Type, targets[1].Target)
	}
	if targets[3].Type != "http" || targets[3].Target != "http://192.168.1.12:9134/metrics" {
		t.Errorf("expected fourth target to be http://192.168.1.12:9134/metrics, got %s/%s", targets[3].Type, targets[3].Target)
	}
}
