# MonStack-Go

Modern infrastructure monitoring platform, written in Go. A complete reimagination of [pndhkm/monstack](https://github.com/pndhkm/monstack).

## Features

- **Single binary** — No Docker, Ansible, or Python required
- **Built-in probers** — HTTP, ICMP, TCP, UDP, DNS, SNMP, gRPC, TLS certificate
- **gRPC agent system** — Lightweight agents with bi-directional streaming
- **Multi-channel alerting** — Telegram, Slack, Discord, Email, Webhook, PagerDuty
- **Auto-discovery** — DNS SRV, Consul, Kubernetes, CIDR scan
- **REST API** — Full CRUD API for all resources
- **Web dashboard** — Embedded real-time monitoring UI
- **Hot-reload config** — Zero-downtime configuration changes
- **Auto TLS** — Built-in ACME / Let's Encrypt
- **Prometheus compatible** — Remote write/read, federation, /metrics endpoint
- **OpenTelemetry** — OTLP ingestion support

## Quick Start

```bash
# Install
curl -fsSL https://your-domain.com/install.sh | bash

# Start server
monstack server --config /etc/monstack/monstack.yaml

# Install agent on target node
monstack-agent --server "monitor.example.com:9443" --token "${AGENT_TOKEN}"
```

## Build from Source

```bash
make build       # Build server
make build-agent # Build agent
make build-all   # Build both
make test        # Run tests
make lint        # Run linters
```

## Documentation

- [Architecture](docs/architecture.md)
- [Getting Started](docs/getting-started.md)
- [Configuration](docs/configuration.md)
- [API Reference](docs/api-reference.md)
- [Migration from monstack](docs/migration-guide.md)

## License

MIT
