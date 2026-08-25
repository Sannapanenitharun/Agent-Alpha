#!/usr/bin/env bash
# Signal collector agent bootstrap installer.
#
#   curl -fsSL https://github.com/OWNER/REPO/releases/latest/download/get.sh \
#     | sudo SIGNAL_INTAKE_URL=... SIGNAL_TENANT_ID=... SIGNAL_INGEST_TOKEN=... bash
#
# Downloads the release binary for this machine, verifies its checksum, and
# hands off to the installer, which creates the service user, config, and
# systemd unit.
set -euo pipefail

# The whole installer lives in a function that is only called on the final line.
# Piping to a shell executes whatever arrives, so a connection cut mid-transfer
# would otherwise run a partial script.
main() {
  REPO="${SIGNAL_REPO:-Sannapanenitharun/Agent-Alpha}"
  VERSION="${SIGNAL_VERSION:-latest}"

  # SIGNAL_DOWNLOAD_BASE overrides where assets come from, for internal mirrors
  # and for testing this script without publishing a release.
  if [ -n "${SIGNAL_DOWNLOAD_BASE:-}" ]; then
    BASE="$SIGNAL_DOWNLOAD_BASE"
  elif [ "$VERSION" = "latest" ]; then
    BASE="https://github.com/$REPO/releases/latest/download"
  else
    BASE="https://github.com/$REPO/releases/download/$VERSION"
  fi

  fail() { echo "error: $*" >&2; exit 1; }

  [ "$(id -u)" -eq 0 ] || fail "must run as root: pipe to 'sudo bash', not 'bash'"

  [ "$(uname -s)" = "Linux" ] || fail "this installer supports Linux; use the container image elsewhere"

  case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac

  if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL "$1" -o "$2"; }
  elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -qO "$2" "$1"; }
  else
    fail "curl or wget is required"
  fi

  # Fail before downloading anything if the install cannot be configured, so a
  # misconfigured run does not leave a half-installed service behind.
  if [ $# -eq 0 ]; then
    [ -n "${SIGNAL_INTAKE_URL:-}" ]   || fail "set SIGNAL_INTAKE_URL (or pass flags after 'bash -s --')"
    [ -n "${SIGNAL_TENANT_ID:-}" ]    || fail "set SIGNAL_TENANT_ID"
    [ -n "${SIGNAL_INGEST_TOKEN:-}" ] || fail "set SIGNAL_INGEST_TOKEN"
  fi

  WORK="$(mktemp -d)"
  trap 'rm -rf "$WORK"' EXIT
  chmod 700 "$WORK"

  BINARY="signal-agent_linux_${ARCH}"
  echo "downloading ${BINARY} from ${BASE}"
  fetch "$BASE/$BINARY" "$WORK/signal-agent" || fail "could not download $BASE/$BINARY"

  # Verify the download. A corrupted or tampered binary must not be installed.
  if fetch "$BASE/checksums.txt" "$WORK/checksums.txt" 2>/dev/null; then
    if command -v sha256sum >/dev/null 2>&1; then
      expected="$(awk -v name="$BINARY" '$2 == name || $2 == "*"name {print $1}' "$WORK/checksums.txt" | head -1)"
      [ -n "$expected" ] || fail "no checksum published for $BINARY"
      actual="$(sha256sum "$WORK/signal-agent" | awk '{print $1}')"
      [ "$expected" = "$actual" ] || fail "checksum mismatch for $BINARY (expected $expected, got $actual)"
      echo "checksum verified"
    else
      echo "warning: sha256sum not available, skipping checksum verification" >&2
    fi
  else
    fail "could not download checksums.txt; refusing to install an unverified binary"
  fi

  fetch "$BASE/install-agent.sh" "$WORK/install-agent.sh" || fail "could not download install-agent.sh"
  chmod +x "$WORK/install-agent.sh"

  if [ $# -gt 0 ]; then
    exec "$WORK/install-agent.sh" --binary "$WORK/signal-agent" "$@"
  fi

  set -- \
    --binary "$WORK/signal-agent" \
    --intake-url "$SIGNAL_INTAKE_URL" \
    --tenant "$SIGNAL_TENANT_ID" \
    --token "$SIGNAL_INGEST_TOKEN"
  [ -n "${SIGNAL_HTTP_ADDR:-}" ] && set -- "$@" --http-addr "$SIGNAL_HTTP_ADDR"
  [ -n "${SIGNAL_GRPC_ADDR:-}" ] && set -- "$@" --grpc-addr "$SIGNAL_GRPC_ADDR"

  exec "$WORK/install-agent.sh" "$@"
}

main "$@"
