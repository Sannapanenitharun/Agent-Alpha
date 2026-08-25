# Signal collector agent

The collector is a small, owned edge agent for customer infrastructure. It accepts all three signal families over authenticated HTTP and forwards tenant-scoped batches to the Signal intake service.

## Local run

```powershell
$env:SIGNAL_INTAKE_URL = "http://localhost:8080/v1/intake"
$env:SIGNAL_TENANT_ID = "tenant-dev"
$env:SIGNAL_INGEST_TOKEN = "replace-me"
go run ./cmd/signal-agent
```

## Install on an EC2 instance

The agent needs a reachable Signal intake URL. Build or publish the image from this repository, then run the agent on the EC2 host. The intake token should be stored in AWS Secrets Manager or SSM Parameter Store for production rather than committed to a shell history.

```bash
# Amazon Linux 2023 / Ubuntu with Docker already installed
docker build -t signal-agent:dev .

docker run -d \
	--name signal-agent \
	--restart unless-stopped \
	-p 4317:4317 \
	-p 4318:4318 \
	-e SIGNAL_INTAKE_URL="https://intake.example.com/v1/intake" \
	-e SIGNAL_TENANT_ID="tenant-production" \
	-e SIGNAL_INGEST_TOKEN="replace-with-secret" \
	signal-agent:dev
```

Verify the process from the EC2 host:

```bash
curl http://127.0.0.1:4318/healthz
docker logs --tail 50 signal-agent
```

The EC2 security group normally only needs outbound HTTPS access to the intake gateway. Open inbound `4317` or `4318` only when applications on other machines must send OTLP directly to this agent. For a single EC2 host, keep both ports bound to localhost and use a local OpenTelemetry Collector or application SDK.

## Collect EC2 host telemetry

The current Signal agent receives and forwards OTLP; it does not yet scrape host metrics or read EC2 log files. Run the OpenTelemetry Collector Contrib beside it to collect host data and forward OTLP locally:

```bash
docker run -d \
	--name otel-host-collector \
	--restart unless-stopped \
	--pid host \
	--net host \
	-e SIGNAL_INGEST_TOKEN="replace-with-secret" \
	-v /:/hostfs:ro \
	-v "$PWD/otel-host-config.yaml:/etc/otelcol-contrib/config.yaml:ro" \
	otel/opentelemetry-collector-contrib:latest
```

The host collector configuration should use `hostmetrics`, `filelog`, and an OTLP exporter targeting `127.0.0.1:4317`. This separation follows the Datadog and Dynatrace pattern: a standard collection distribution gathers host data, while the Signal agent owns tenant authentication and cloud forwarding.

The OTLP HTTP listener defaults to `:4318` and the OTLP gRPC listener defaults to `:4317`. Override them with `SIGNAL_AGENT_LISTEN_ADDRESS` and `SIGNAL_AGENT_GRPC_LISTEN_ADDRESS`.

## Receiver contract

- `POST /v1/logs`
- `POST /v1/metrics`
- `POST /v1/traces`
- `GET /healthz`
- `GET /readyz`

OTLP gRPC services are available on the gRPC listener for logs, metrics, and traces using the standard `Export*ServiceRequest` contracts.

Send `Authorization: Bearer <ingest-token>` or `X-Signal-Ingest-Token`. The preferred content type is `application/x-protobuf` with the matching OTLP `Export*ServiceRequest` message. `application/json` is also accepted using OTLP JSON encoding. Untyped JSON remains temporarily supported for migration compatibility. OTLP payloads are normalized to JSON inside the tenant envelope before forwarding to intake. The agent limits each payload to 10 MiB, bounds its in-memory queue, returns `503` when backpressured, batches by size or time, and retries intake delivery three times with exponential backoff.

## Production hardening before GA

Implement mTLS or signed short-lived agent credentials, persistent disk buffering, remote configuration, config reload, compression, payload redaction, rate telemetry, and a dead-letter path.

## Compression

Both transports accept gzip, which standard OTLP exporters enable by default:

- HTTP: `Content-Encoding: gzip` is decompressed, bounded at 10 MiB before and after inflation.
- gRPC: the gzip decompressor is registered, so exporters using the default
  `compression: gzip` setting are accepted.

Without this, an OTel exporter fails with
`Unimplemented: grpc: Decompressor is not installed for grpc-encoding "gzip"`.

## EC2 reference deployment

Deployed on an Ubuntu 24.04 EC2 host. All listeners bind `127.0.0.1`;
nothing is publicly exposed and no security-group change is required.

| Component | Address | Managed by |
|---|---|---|
| signal-agent OTLP HTTP | `127.0.0.1:14318` | `signal-agent.service` |
| signal-agent OTLP gRPC | `127.0.0.1:14317` | `signal-agent.service` |
| signal-intake | `127.0.0.1:18080` | `signal-intake.service` |
| host metrics + logs scraper | otelcol-contrib | `signal-hostmetrics` container |

Non-default ports are required because `4317`, `4318`, and `8080` are already
taken on that host by `grafana/otel-lgtm` and `nginx-api-monitoring`.

Layout:

```text
/opt/signal/bin/{signal-agent,signal-intake}   binaries
/etc/signal/{agent,intake}.env                 credentials, 0640 root:signal
/etc/signal/otel/host-config.yaml              scraper config
/var/lib/signal/telemetry.jsonl                stored telemetry
```

Both services run as the unprivileged `signal` user with `ProtectSystem=strict`
and `NoNewPrivileges`. To view the data locally:

```bash
ssh -i <key>.pem -L 18080:127.0.0.1:18080 ubuntu@<ec2-host>
SIGNAL_API_URL=http://localhost:18080 SIGNAL_API_TOKEN=<token> npm run dev
```

The `process` scraper cannot read `/proc/1/exe` without `CAP_SYS_PTRACE`; other
host metrics are unaffected.

## Dashboard credentials

The dashboard reads telemetry through its own server-side proxy at
`/api/signal`, which holds the intake token and forwards only the read-only
`summary` and `telemetry` endpoints. Configure it with `SIGNAL_API_URL` and
`SIGNAL_API_TOKEN`.

Never use a `NEXT_PUBLIC_` prefix for the token. Next.js inlines those into the
client bundle, and the intake token is a write credential that also authenticates
`POST /v1/intake`. Routing through the proxy also keeps the browser request
same-origin, which the intake service requires: it serves no CORS headers and
answers preflight `OPTIONS` with 405.

## Pipeline self-telemetry

`GET /v1/stats` on the collector reports `queued`, `queue_capacity`,
`delivered`, and `dropped`. It requires the ingest token.

## Tenants

The intake gateway resolves a request's tenant from the bearer token it
presents. Configure the registry with `SIGNAL_INTAKE_TENANTS_FILE`, a JSON array:

```json
[
  {"id": "acme",   "token": "..."},
  {"id": "globex", "token": "..."}
]
```

Two tenants may not share a token — a shared token cannot identify a tenant, so
the service refuses to start. For local development, `SIGNAL_INTAKE_TOKEN` plus
`SIGNAL_INTAKE_TENANT_ID` still configure a single tenant.

An envelope may carry a `tenant_id`, but it is a claim, not identity: if it
disagrees with the authenticated tenant the request is rejected with 400.
Queries are scoped the same way, so one tenant's credential cannot read
another's telemetry.

## Installing the agent

### One line

```bash
curl -fsSL https://github.com/Sannapanenitharun/Agent-Alpha/releases/latest/download/get.sh   | sudo SIGNAL_INTAKE_URL=https://intake.example.com/v1/intake          SIGNAL_TENANT_ID=acme          SIGNAL_INGEST_TOKEN=your-token     bash
```

The bootstrap script detects the architecture, downloads the matching release
binary, verifies its SHA-256 against the published `checksums.txt`, and refuses
to install if the checksum does not match or the file cannot be verified.

| Variable | Purpose |
|---|---|
| `SIGNAL_INTAKE_URL` | Required. Where the agent forwards telemetry |
| `SIGNAL_TENANT_ID` | Required. Tenant this agent reports as |
| `SIGNAL_INGEST_TOKEN` | Required. Ingest token for that tenant |
| `SIGNAL_HTTP_ADDR` | OTLP HTTP listen address (default `127.0.0.1:4318`) |
| `SIGNAL_GRPC_ADDR` | OTLP gRPC listen address (default `127.0.0.1:4317`) |
| `SIGNAL_VERSION` | Pin a release, e.g. `v0.1.0` (default `latest`) |
| `SIGNAL_DOWNLOAD_BASE` | Fetch assets from an internal mirror instead |

Flags work too, for anything the variables do not cover:

```bash
curl -fsSL .../get.sh | sudo bash -s --   --intake-url https://intake.example.com/v1/intake   --tenant acme --token your-token --no-start
```

Pin `SIGNAL_VERSION` in automation. `latest` moves, so an unpinned install is
not reproducible.

### Linux with systemd, from a checkout

```bash
sudo ./scripts/install-agent.sh   --intake-url https://intake.example.com/v1/intake   --tenant acme   --token "$SIGNAL_INGEST_TOKEN"
```

Run from a source checkout it builds the binary itself; otherwise pass
`--binary /path/to/signal-agent`. Re-running upgrades in place: the binary is
replaced and the service restarted.

Listen addresses default to `127.0.0.1:4318` (HTTP) and `127.0.0.1:4317` (gRPC).
Override with `--http-addr` / `--grpc-addr`, and bind beyond loopback only with
TLS in front. Use `--no-start` to stage an install without enabling the service.

The installer creates an unprivileged `signal` user, writes
`/etc/signal/agent.env` as `0640 root:signal`, and runs the service with
`NoNewPrivileges`, `ProtectSystem=strict`, and `ProtectHome`.

```bash
systemctl status signal-agent
journalctl -u signal-agent -f
```

### Container

```bash
docker run -d --name signal-agent   -p 4317:4317 -p 4318:4318   -e SIGNAL_INTAKE_URL=https://intake.example.com/v1/intake   -e SIGNAL_TENANT_ID=acme   -e SIGNAL_INGEST_TOKEN="$SIGNAL_INGEST_TOKEN"   -e SIGNAL_AGENT_LISTEN_ADDRESS=:4318   -e SIGNAL_AGENT_GRPC_LISTEN_ADDRESS=:4317   signal-agent:latest
```

Build the image with `docker build -t signal-agent:latest .`.

On PowerShell the `\` line continuations above are not understood — it uses a
backtick instead. Simplest is to keep it on one line:

```powershell
docker run -d --name signal-agent -p 4317:4317 -p 4318:4318 -e SIGNAL_INTAKE_URL=https://intake.example.com/v1/intake -e SIGNAL_TENANT_ID=acme -e SIGNAL_INGEST_TOKEN=$env:SIGNAL_INGEST_TOKEN -e SIGNAL_AGENT_LISTEN_ADDRESS=:4318 -e SIGNAL_AGENT_GRPC_LISTEN_ADDRESS=:4317 signal-agent:latest
```

Note `$env:NAME` rather than `$NAME` for environment variables. If the compose
stack is already running it owns 4317 and 4318, so either stop it first or map
different host ports, for example `-p 14317:4317 -p 14318:4318`.

### Uninstall

```bash
sudo systemctl disable --now signal-agent
sudo rm -rf /opt/signal/bin/signal-agent /etc/signal/agent.env             /etc/systemd/system/signal-agent.service
sudo systemctl daemon-reload
```
