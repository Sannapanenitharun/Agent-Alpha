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
