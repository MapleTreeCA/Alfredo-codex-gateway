#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

MODE="${1:-fg}"
LOG_FILE="${GATEWAY_LOG_FILE:-/tmp/codex-gateway.log}"

if [[ "${GATEWAY_MLX_WHISPER_BIN:-}" == "" ]]; then
  if command -v mlx_whisper >/dev/null 2>&1; then
    GATEWAY_MLX_WHISPER_BIN="$(command -v mlx_whisper)"
  elif [[ -x "$HOME/.local/bin/mlx_whisper" ]]; then
    GATEWAY_MLX_WHISPER_BIN="$HOME/.local/bin/mlx_whisper"
  else
    echo "mlx_whisper not found. Set GATEWAY_MLX_WHISPER_BIN first."
    exit 1
  fi
fi

export GATEWAY_LISTEN_ADDR="${GATEWAY_LISTEN_ADDR:-0.0.0.0:18910}"
export GATEWAY_WS_PATH="${GATEWAY_WS_PATH:-/ws}"
export GATEWAY_WS_TOKEN="${GATEWAY_WS_TOKEN:-}"
export GATEWAY_LLM_PROVIDER="${GATEWAY_LLM_PROVIDER:-codex}"
export GATEWAY_STT_PROVIDER="${GATEWAY_STT_PROVIDER:-local}"
export GATEWAY_TTS_PROVIDER="${GATEWAY_TTS_PROVIDER:-local}"
export CODEX_MAX_OUTPUT_TOKENS="${CODEX_MAX_OUTPUT_TOKENS:-1000}"
export GATEWAY_SESSION_SILENCE="${GATEWAY_SESSION_SILENCE:-900ms}"
export GATEWAY_SESSION_MAX_TURN="${GATEWAY_SESSION_MAX_TURN:-12s}"
export GATEWAY_TTS_MAX_DURATION="${GATEWAY_TTS_MAX_DURATION:-180s}"
export GATEWAY_STT_STREAMING_ENABLED="${GATEWAY_STT_STREAMING_ENABLED:-true}"
export GATEWAY_STT_INTERIM_INTERVAL="${GATEWAY_STT_INTERIM_INTERVAL:-600ms}"
export GATEWAY_STT_INTERIM_MIN_AUDIO="${GATEWAY_STT_INTERIM_MIN_AUDIO:-700ms}"
export GATEWAY_STT_LANGUAGE="${GATEWAY_STT_LANGUAGE:-en}"
export GATEWAY_RUNTIME_CONFIG_RESET_ON_START="${GATEWAY_RUNTIME_CONFIG_RESET_ON_START:-false}"
# CoreS3 firmware playback queue is tuned for 60ms Opus frames. Using 20ms
# greatly increases packet rate and can overflow decode queue under long replies.
export GATEWAY_OPUS_FRAME_DURATION_MS="${GATEWAY_OPUS_FRAME_DURATION_MS:-60}"
export GATEWAY_DOWNLINK_SAMPLE_RATE="${GATEWAY_DOWNLINK_SAMPLE_RATE:-24000}"
export GATEWAY_DOWNLINK_OPUS_BITRATE="${GATEWAY_DOWNLINK_OPUS_BITRATE:-32000}"
export OPENAI_STT_LANGUAGE="${OPENAI_STT_LANGUAGE:-$GATEWAY_STT_LANGUAGE}"
export OPENAI_STT_TIMEOUT="${OPENAI_STT_TIMEOUT:-90s}"
export OPENAI_TTS_TIMEOUT="${OPENAI_TTS_TIMEOUT:-90s}"
export GATEWAY_MLX_WHISPER_BIN
export GATEWAY_MLX_WHISPER_MODEL="${GATEWAY_MLX_WHISPER_MODEL:-mlx-community/whisper-large-v3-turbo}"
export GATEWAY_MLX_WHISPER_RESIDENT_ENABLED="${GATEWAY_MLX_WHISPER_RESIDENT_ENABLED:-true}"
export GATEWAY_MLX_WHISPER_RESIDENT_TIMEOUT="${GATEWAY_MLX_WHISPER_RESIDENT_TIMEOUT:-8s}"
export GATEWAY_LOCAL_STT_ADDR="${GATEWAY_LOCAL_STT_ADDR:-127.0.0.1:19610}"
export GATEWAY_LOCAL_TTS_ADDR="${GATEWAY_LOCAL_TTS_ADDR:-127.0.0.1:19611}"
export GATEWAY_LOCAL_TTS_VOICE="${GATEWAY_LOCAL_TTS_VOICE:-Daniel}"
export GATEWAY_LOCAL_TTS_RATE="${GATEWAY_LOCAL_TTS_RATE:-180}"
export GATEWAY_LOCAL_TTS_SAMPLE_RATE="${GATEWAY_LOCAL_TTS_SAMPLE_RATE:-24000}"
export GATEWAY_LOCAL_MODULE_STARTUP_TIMEOUT="${GATEWAY_LOCAL_MODULE_STARTUP_TIMEOUT:-10s}"
export GATEWAY_MEMORY_DIR="${GATEWAY_MEMORY_DIR:-$HOME/.gateway-memory}"
export GATEWAY_MEMORY_CONTEXT_SIZE="${GATEWAY_MEMORY_CONTEXT_SIZE:-10}"

echo "gateway config:"
echo "  listen=${GATEWAY_LISTEN_ADDR} ws_path=${GATEWAY_WS_PATH} ws_token=${GATEWAY_WS_TOKEN:+set}"
echo "  llm=${GATEWAY_LLM_PROVIDER} stt=${GATEWAY_STT_PROVIDER} tts=${GATEWAY_TTS_PROVIDER}"
echo "  max_output_tokens=${CODEX_MAX_OUTPUT_TOKENS}"
echo "  silence=${GATEWAY_SESSION_SILENCE} max_turn=${GATEWAY_SESSION_MAX_TURN} tts_max_duration=${GATEWAY_TTS_MAX_DURATION} stt_streaming=${GATEWAY_STT_STREAMING_ENABLED} interval=${GATEWAY_STT_INTERIM_INTERVAL} min_audio=${GATEWAY_STT_INTERIM_MIN_AUDIO} stt_lang=${OPENAI_STT_LANGUAGE}"
echo "  opus_frame_ms=${GATEWAY_OPUS_FRAME_DURATION_MS} downlink_sample_rate=${GATEWAY_DOWNLINK_SAMPLE_RATE} downlink_bitrate=${GATEWAY_DOWNLINK_OPUS_BITRATE}"
echo "  stt_timeout=${OPENAI_STT_TIMEOUT} tts_timeout=${OPENAI_TTS_TIMEOUT} runtime_reset_on_start=${GATEWAY_RUNTIME_CONFIG_RESET_ON_START}"
echo "  stt_addr=${GATEWAY_LOCAL_STT_ADDR} tts_addr=${GATEWAY_LOCAL_TTS_ADDR}"
echo "  tts_voice=${GATEWAY_LOCAL_TTS_VOICE} tts_rate=${GATEWAY_LOCAL_TTS_RATE} sample_rate=${GATEWAY_LOCAL_TTS_SAMPLE_RATE}"
echo "  mlx_whisper=${GATEWAY_MLX_WHISPER_BIN} model=${GATEWAY_MLX_WHISPER_MODEL}"
echo "  mlx_resident=${GATEWAY_MLX_WHISPER_RESIDENT_ENABLED} timeout=${GATEWAY_MLX_WHISPER_RESIDENT_TIMEOUT}"

if [[ -n "${GATEWAY_BIN:-}" ]]; then
  RUN_CMD=("${GATEWAY_BIN}")
elif [[ -x ./bin/codex-gateway ]]; then
  RUN_CMD=(./bin/codex-gateway)
elif [[ -x ./codex-gateway ]]; then
  RUN_CMD=(./codex-gateway)
else
  RUN_CMD=(go run ./cmd/codex-gateway)
fi

if [[ "$MODE" == "--daemon" || "$MODE" == "daemon" ]]; then
  nohup "${RUN_CMD[@]}" >"$LOG_FILE" 2>&1 &
  echo "gateway started in background: pid=$! log=$LOG_FILE"
  exit 0
fi

exec "${RUN_CMD[@]}"
