# Migrating from Original `pndhkm/monstack`

MonStack-Go includes a built-in CLI migration tool that converts your existing `prometheus.yml` scrape jobs (`icmp`, `snmp`, `http`, `zfs-exporter`, `bird_exporter`) into native MonStack-Go target definitions:

```bash
./bin/monstack migrate \
  --from-prometheus /opt/monstack/prometheus/prometheus.yml \
  --output migrated-targets.yaml
```

## Replacing Linux / ZFS / BIRD Exporters

Instead of Ansible roles for `pmm-agent`, `zfs_exporter`, and `bird_exporter`, deploy the single `monstack-agent` binary on target hosts:

```bash
monstack-agent \
  --server monitor.example.com:9443 \
  --collectors cpu,memory,disk,network,load,zfs,bird
```
