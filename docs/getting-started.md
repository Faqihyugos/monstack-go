# Getting Started

## Build from Source

```bash
git clone https://github.com/Faqihyugos/monstack-go.git
cd monstack-go
make build-all
```

This produces two static, CGO-free binaries:
- `bin/monstack` — Monitoring server + CLI
- `bin/monstack-agent` — Lightweight host agent

## Start the Server

```bash
./bin/monstack config validate --config configs/examples/monstack.yaml
./bin/monstack server --config configs/examples/monstack.yaml
```

## Connect an Agent

```bash
./bin/monstack-agent \
  --server 127.0.0.1:9443 \
  --collectors cpu,memory,disk,network,load,zfs,bird \
  --interval 10s
```

## Verify Health & Metrics

```bash
curl -k https://127.0.0.1:8443/api/v1/health
curl -k https://127.0.0.1:8443/metrics
```
