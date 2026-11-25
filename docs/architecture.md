# MonStack-Go Architecture

MonStack-Go is a single-binary, CGO-free Go infrastructure monitoring platform designed as a modern evolution of [pndhkm/monstack](https://github.com/pndhkm/monstack).

## High-Level Components

1. **Prober Engine (`internal/prober`)**: Native Go probers for HTTP/HTTPS, ICMP, TCP, UDP, DNS, TLS, gRPC Health, and SNMP v1/v2c/v3 — zero external exporters needed.
2. **Scheduler (`internal/scheduler`)**: Bounded goroutine worker pool with per-target interval tickers and immediate reconciliation on REST API CRUD changes.
3. **Storage (`internal/storage`)**:
   - **Metadata**: Pure-Go SQLite (`modernc.org/sqlite`) with WAL mode and automated schema migrations (`migrations/`).
   - **Time-Series (TSDB)**: In-memory series store with append-only Write-Ahead Log (WAL) crash recovery and retention pruning.
4. **Alerting (`internal/alerting`)**: Expression evaluator (`pending -> firing -> resolved` with `for` duration), SQLite silences, label-based routing, and 6 built-in channels (Telegram, Slack, Discord, Webhook, Email, PagerDuty).
5. **Agent System (`internal/collector`, `internal/agent`, `cmd/agent`)**: Bi-directional HTTP/2 gRPC streaming for host metrics (CPU, Memory, Disk, Network, Load, ZFS, BIRD) and remote command push.
6. **Discovery (`internal/discovery`)**: Automatic target discovery via DNS SRV, CIDR port sweep, and external YAML/JSON files.
7. **REST API & Web UI (`api/rest`, `web`)**: Chi v5 HTTP/HTTPS server (with ACME auto-TLS / self-signed / manual certs), RBAC users, audit log export, Prometheus `/metrics`, and embedded HTML dashboard via `embed.FS`.
