#!/usr/bin/env bash
# Install the Signal collector agent as a systemd service.
#
#   sudo ./install-agent.sh --intake-url URL --tenant ID --token TOKEN
#
# Re-running upgrades an existing installation in place: the binary is replaced
# and the service restarted, with configuration preserved unless overridden.
set -euo pipefail

INTAKE_URL=""
TENANT_ID=""
TOKEN=""
HTTP_ADDR="127.0.0.1:4318"
GRPC_ADDR="127.0.0.1:4317"
BINARY=""
SOURCE=""
START=1

PREFIX=/opt/signal
CONFIG_DIR=/etc/signal
UNIT=/etc/systemd/system/signal-agent.service
SERVICE_USER=signal

usage() {
  cat <<'USAGE'
Usage: install-agent.sh [options]

Required:
  --intake-url URL     Where the agent forwards telemetry
                       (e.g. https://intake.example.com/v1/intake)
  --tenant ID          Tenant this agent reports as
  --token TOKEN        Ingest token for that tenant

Optional:
  --http-addr ADDR     OTLP HTTP listen address   (default 127.0.0.1:4318)
  --grpc-addr ADDR     OTLP gRPC listen address   (default 127.0.0.1:4317)
  --binary PATH        Install a prebuilt binary instead of building
  --source DIR         Build from a source checkout (default: repo containing
                       this script, when present)
  --no-start           Install without enabling or starting the service
  -h, --help           Show this message

The listen addresses default to loopback. Bind 0.0.0.0 only if telemetry
arrives from other hosts, and put TLS in front of it when it does.
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --intake-url) INTAKE_URL="$2"; shift 2 ;;
    --tenant)     TENANT_ID="$2";  shift 2 ;;
    --token)      TOKEN="$2";      shift 2 ;;
    --http-addr)  HTTP_ADDR="$2";  shift 2 ;;
    --grpc-addr)  GRPC_ADDR="$2";  shift 2 ;;
    --binary)     BINARY="$2";     shift 2 ;;
    --source)     SOURCE="$2";     shift 2 ;;
    --no-start)   START=0;         shift ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

fail() { echo "error: $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "must run as root (use sudo)"
[ -n "$INTAKE_URL" ] || fail "--intake-url is required"
[ -n "$TENANT_ID" ]  || fail "--tenant is required"
[ -n "$TOKEN" ]      || fail "--token is required"

case "$(uname -s)" in
  Linux) ;;
  *) fail "this installer targets Linux with systemd; use the container image elsewhere" ;;
esac

# ---------------------------------------------------------------- obtain binary
if [ -z "$BINARY" ]; then
  if [ -z "$SOURCE" ]; then
    candidate="$(cd "$(dirname "$0")/.." 2>/dev/null && pwd || true)"
    if [ -n "$candidate" ] && [ -f "$candidate/go.mod" ]; then
      SOURCE="$candidate"
    fi
  fi
  [ -n "$SOURCE" ] || fail "provide --binary PATH or --source DIR"
  command -v go >/dev/null 2>&1 || fail "Go is required to build from source; use --binary instead"
  echo "building signal-agent from $SOURCE"
  BINARY="$(mktemp -d)/signal-agent"
  ( cd "$SOURCE" && CGO_ENABLED=0 go build -ldflags="-s -w" -o "$BINARY" ./cmd/signal-agent )
fi
[ -f "$BINARY" ] || fail "binary not found: $BINARY"

# ------------------------------------------------------------------- install
id -u "$SERVICE_USER" >/dev/null 2>&1 || \
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"

install -d -o root -g root          -m 755 "$PREFIX/bin"
install -d -o root -g "$SERVICE_USER" -m 750 "$CONFIG_DIR"
install -o root -g root -m 755 "$BINARY" "$PREFIX/bin/signal-agent"

# 0640 root:signal — readable by the service, not by other local users.
umask 077
cat > "$CONFIG_DIR/agent.env" <<ENV
SIGNAL_AGENT_LISTEN_ADDRESS=$HTTP_ADDR
SIGNAL_AGENT_GRPC_LISTEN_ADDRESS=$GRPC_ADDR
SIGNAL_INTAKE_URL=$INTAKE_URL
SIGNAL_TENANT_ID=$TENANT_ID
SIGNAL_INGEST_TOKEN=$TOKEN
ENV
chown root:"$SERVICE_USER" "$CONFIG_DIR/agent.env"
chmod 640 "$CONFIG_DIR/agent.env"

cat > "$UNIT" <<'ENV'
[Unit]
Description=Signal OTLP collector agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=signal
Group=signal
EnvironmentFile=/etc/signal/agent.env
ExecStart=/opt/signal/bin/signal-agent
Restart=always
RestartSec=3
# The agent only receives OTLP over the network; it needs no host access.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
# Allow the drain on shutdown to finish before the process is killed.
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
ENV
chmod 644 "$UNIT"

if [ "$START" -eq 1 ] && command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  systemctl daemon-reload
  systemctl enable --now signal-agent.service
  sleep 2
  if ! systemctl is-active --quiet signal-agent.service; then
    echo "service failed to start; recent log:" >&2
    journalctl -u signal-agent -n 20 --no-pager >&2 || true
    exit 1
  fi
  echo "signal-agent is running"
else
  echo "installed without starting (no systemd, or --no-start)"
fi

cat <<DONE

Signal collector agent installed.

  OTLP HTTP   $HTTP_ADDR
  OTLP gRPC   $GRPC_ADDR
  forwards to $INTAKE_URL
  tenant      $TENANT_ID

  binary      $PREFIX/bin/signal-agent
  config      $CONFIG_DIR/agent.env
  logs        journalctl -u signal-agent -f

Send a test event:
  curl -X POST http://$HTTP_ADDR/v1/logs \
    -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
    -d '{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"body":{"stringValue":"hello"}}]}]}]}'
DONE
