# Signal

A multi-tenant observability platform: metrics, logs, and traces collected at
the edge over OTLP, forwarded to a tenant-authenticated intake gateway, and
served back through a query API and operator dashboard.

Signal is built on OpenTelemetry rather than a proprietary wire format. The
differentiation is in tenant isolation, enrichment, routing, fleet management,
and the product experience — not in reinventing receivers.

## Components

| Component | Path | Role |
|---|---|---|
| Collector agent | [cmd/signal-agent](cmd/signal-agent) | Receives OTLP over HTTP and gRPC, batches, retries, forwards |
| Intake gateway | [cmd/signal-intake](cmd/signal-intake) | Authenticates tenants, validates, persists, serves queries |
| AWS collector | [cmd/signal-aws-collector](cmd/signal-aws-collector) | Agentless collection from CloudWatch, EC2, ECS, CloudTrail, and service adapters |
| Dashboard | [src/app](src/app) | Next.js operator UI, reads through a server-side proxy |
| Wire contract | [internal/telemetry](internal/telemetry) | Event and Envelope types shared by all services |

## Quick start

```bash
docker compose up --build
```

That starts the collector on `4317`/`4318` and the intake gateway on `8080`.
Send OTLP to the collector:

```bash
curl -X POST http://localhost:4318/v1/logs \
  -H "Authorization: Bearer local-only-token" \
  -H "Content-Type: application/json" \
  -d '{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"body":{"stringValue":"hello"}}]}]}]}'
```

Run the dashboard against it:

```bash
SIGNAL_API_URL=http://localhost:8080 SIGNAL_API_TOKEN=local-only-token npm run dev
```

## Installing the agent on a host

```bash
curl -fsSL https://github.com/Sannapanenitharun/Agent-Alpha/releases/latest/download/get.sh   | sudo SIGNAL_INTAKE_URL=https://intake.example.com/v1/intake          SIGNAL_TENANT_ID=acme SIGNAL_INGEST_TOKEN=your-token bash
```

Or from a checkout:

```bash
sudo ./scripts/install-agent.sh   --intake-url https://intake.example.com/v1/intake   --tenant acme --token "$SIGNAL_INGEST_TOKEN"
```

See [COLLECTOR.md](COLLECTOR.md) for options, the container equivalent, and
uninstall steps.

## Tests

```bash
go test ./...      # collector, intake, AWS collector
npm run build      # dashboard type check and build
```

## Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) — how the pieces fit together and why
- [COLLECTOR.md](COLLECTOR.md) — collector protocol, deployment, and operations
- [AWS_COVERAGE.md](AWS_COVERAGE.md) — which AWS services are collected

## Status

Working today: OTLP ingestion over both transports, gzip, tenant-authenticated
intake with per-tenant isolation, indexed queries, the AWS collector, and the
dashboard.

Not yet built: durable scalable storage (ClickHouse for logs and traces, a
Prometheus-compatible backend for metrics), alert evaluation, the service
catalog, and collector fleet management. Telemetry currently persists to JSON
Lines behind a storage interface intended to be swapped.
