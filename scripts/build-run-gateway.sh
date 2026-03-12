#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

usage() {
  cat <<'EOF'
Usage: scripts/build-run-gateway.sh [--daemon|--fg] [--no-build]

Options:
  --daemon   Build, then run in background.
  --fg       Build, then run in foreground (default).
  --no-build Skip build and only run the existing binary.
  --help     Show this help.
EOF
}

MODE="fg"
DO_BUILD="true"

export GATEWAY_LLM_PROVIDER="${GATEWAY_LLM_PROVIDER:-codex}"
export GATEWAY_STT_PROVIDER="${GATEWAY_STT_PROVIDER:-local}"
export GATEWAY_TTS_PROVIDER="${GATEWAY_TTS_PROVIDER:-local}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --daemon|daemon)
      MODE="daemon"
      ;;
    --fg|fg)
      MODE="fg"
      ;;
    --no-build)
      DO_BUILD="false"
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
  shift
done

mkdir -p bin logs

STAMP="$(date +%Y%m%d-%H%M%S)"
BUILD_LOG_FILE="${GATEWAY_BUILD_LOG_FILE:-$ROOT_DIR/logs/build-$STAMP.log}"
RUN_LOG_FILE="${GATEWAY_LOG_FILE:-$ROOT_DIR/logs/gateway-$STAMP.log}"
export GATEWAY_BIN="${GATEWAY_BIN:-$ROOT_DIR/bin/codex-gateway}"
export GATEWAY_LOG_FILE="$RUN_LOG_FILE"

if [[ "$DO_BUILD" == "true" ]]; then
  echo "building gateway -> $GATEWAY_BIN"
  echo "build log -> $BUILD_LOG_FILE"
  go build -o "$GATEWAY_BIN" ./cmd/codex-gateway 2>&1 | tee "$BUILD_LOG_FILE"
else
  echo "skipping build; using existing binary -> $GATEWAY_BIN"
fi

echo "run log -> $RUN_LOG_FILE"

if [[ "$MODE" == "daemon" ]]; then
  exec "$ROOT_DIR/scripts/start-alfredo-gateway.sh" --daemon
fi

exec "$ROOT_DIR/scripts/start-alfredo-gateway.sh"
