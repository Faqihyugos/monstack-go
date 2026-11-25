# Configuration Reference

MonStack-Go uses YAML configuration with live hot-reload via `fsnotify`, `SIGHUP`, or `PUT /api/v1/config/reload`. Environment variables prefixed with `MONSTACK_` override any field (e.g. `MONSTACK_SERVER_LISTEN=:8080`).

See [configs/examples/monstack.yaml](../configs/examples/monstack.yaml) for a full annotated example.

## Key Sections

- `server`: `listen`, `mode` (`http` | `https` | `auto`), `tls.mode` (`self-signed` | `acme` | `manual`)
- `auth`: `enabled`, `admin_user`, `admin_password` / `admin_password_hash`, `api_keys`, `jwt`
- `storage`: `tsdb.path`, `tsdb.retention`, `tsdb.max_disk`, `metadata.path`
- `grpc`: `listen` (e.g. `:9443`), `tls`, `max_recv_msg_size`
- `probes`: `defaults` + `targets` supporting `http`, `icmp`, `tcp`, `udp`, `dns`, `snmp`, `grpc`, `tls`
- `alerting`: `evaluation_interval`, `rules`, `channels` (`telegram`, `slack`, `discord`, `email`, `pagerduty`, `webhook`), `routing`
- `discovery`: `dns_srv`, `cidr_scan`, `static`
- `logging`: `level` (`debug` | `info` | `warn` | `error`), `format` (`json` | `text`), `output` (`stdout` | `stderr` | `file`)
