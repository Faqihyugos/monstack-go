# REST API Reference

Base URL: `https://<host>:8443`

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/` | Embedded Web Dashboard |
| `GET` | `/metrics` | Prometheus self-monitoring metrics |
| `GET` | `/api/v1/health` | Subsystem health status |
| `GET` | `/api/v1/config` | Running config (secrets redacted) |
| `PUT` | `/api/v1/config/reload` | Trigger configuration hot-reload |
| `GET` | `/api/v1/targets` | List targets (`?type=&enabled=`) |
| `POST` | `/api/v1/targets` | Create target |
| `GET` | `/api/v1/targets/{id}` | Get target by ID |
| `PUT` | `/api/v1/targets/{id}` | Update target |
| `DELETE` | `/api/v1/targets/{id}` | Delete target |
| `GET` | `/api/v1/targets/{id}/status` | Latest probe result |
| `GET` | `/api/v1/targets/{id}/metrics` | Target TSDB metrics (`?metric=&start=&end=&step=`) |
| `GET` | `/api/v1/agents` | List registered gRPC agents |
| `GET` | `/api/v1/agents/{id}` | Agent detail |
| `DELETE` | `/api/v1/agents/{id}` | Unregister agent |
| `POST` | `/api/v1/agents/{id}/command` | Push remote command to connected agent |
| `GET` | `/api/v1/alerts` | List firing alerts (`?status=all`) |
| `GET/POST` | `/api/v1/alerts/rules` | List or create alert rules |
| `GET/PUT/DELETE` | `/api/v1/alerts/rules/{id}` | Manage alert rule |
| `POST` | `/api/v1/alerts/silence` | Create alert silence |
| `GET` | `/api/v1/alerts/silences` | List active silences |
| `DELETE` | `/api/v1/alerts/silences/{id}` | Delete silence |
| `GET` | `/api/v1/query` | Instant metric query (`?target=&metric=`) |
| `GET` | `/api/v1/query_range` | Range metric query |
| `POST` | `/api/v1/import` | Import JSON metric batch into TSDB |
| `GET` | `/api/v1/audit` | Query audit log (`?resource=&limit=`) |
| `GET` | `/api/v1/audit/export` | Export audit log as NDJSON |
| `GET/POST` | `/api/v1/users` | List or create RBAC users |
| `DELETE` | `/api/v1/users/{id}` | Delete RBAC user |
