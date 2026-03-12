# Voice Gateway For `alfredo-esp32`

This gateway keeps the `alfredo-esp32` WebSocket protocol unchanged.

## Alfredo Series

- Organization: <https://github.com/MapleTreeCA>
- Firmware (`alfredo-esp32`): <https://github.com/MapleTreeCA/alfredo-esp32>
- Gateway (`alfredo-codex-gateway`): <https://github.com/MapleTreeCA/alfredo-codex-gateway>

Current phase focus:

- built-in ChatGPT OAuth flow inside gateway
- built-in web console to login/authorize
- web text/voice test page for quick dialogue verification
- websocket STT emits both `state=interim` (streaming) and `state=final`

## Development Loadmap

- Full roadmap file: [dev/dev-loadmap.md](dev/dev-loadmap.md)
- This section is an embedded preview to keep roadmap context visible in README.

### Embedded Preview

- Project: Alfredo is an ESP32-based hardware agent powered by this `codex-gateway`.
- Core capabilities: voice interaction, expressions, vision, and physical articulation.
- 2026 completed milestones: hardware selection, desktop Alfred app, initial gateway/firmware, expression system, local memory + STT/TTS in gateway.
- Phase 1 (in progress): websocket voice stream stability, turn-taking flow, expression trigger migration, animation smoothness.
- Phase 2: token/memory optimization, local skills (memo/calendar/mail), local-first vision modules.
- Phase 3: servo calibration + 3D chassis integration.
- Phase 4: MCP expansion, coding workflow automation, GSM/cloud deployment, multi-terminal support.
- Latest debug focus: firmware-to-gateway boot path, protocol selection priority, OTA bypass, and CoreS3 default websocket alignment.

## Built-In Web Console

After startup:

- UI: `http://127.0.0.1:18910/`
- Health: `http://127.0.0.1:18910/healthz`

The console supports:

- OAuth login (`/oauth2/initiate` -> `/oauth2/callback`)
- Runtime config panel with dropdown selectors (model, effort, verbosity, tools, TTS voice, TTS rate)
- Context memory depth control (`context_messages`) to set how many recent messages are sent to Codex per turn
- Built-in diagnostics line in UI for per-turn token usage + memory message count (can be hidden)
- Dynamic model catalog from codex API (with static fallback)
- Runtime config auto-load on page open, and live reload after save
- Text chat test (`/api/chat`) with selectable runtime options
- Voice upload STT test (`/api/transcribe`)
- Optional TTS playback (`/api/tts`)
- Persistent conversation memory on disk via SQLite
- Dedicated memory search page: `http://127.0.0.1:18910/memory.html`
- Device SD runtime config push panel (select a connected device, edit JSON, write to `/sdcard/alfredo.cfg`)

## Providers (current defaults)

- LLM: `codex`
- STT: `openai` (can switch to `mlx-whisper` / `local`)
- TTS: `openai` (can switch to `local`)

`local` mode means:

- gateway starts built-in local STT module (`/transcribe`)
- gateway starts built-in local TTS module (`/synthesize`)
- local STT keeps a resident `mlx_whisper` python worker to reuse loaded model (can disable by env)
- gateway voice pipeline streams TTS in sentence-sized chunks to reduce first-audio latency
- startup checks module health; if not running, it starts them automatically

## Requirements

- Go `1.25+`
- `libopus` installed locally
- `OPENAI_API_KEY` when using OpenAI STT/TTS
- If STT provider is `mlx-whisper`: install `mlx_whisper`
- If STT provider is `local`: install `mlx_whisper`
- If TTS provider is `local`: macOS `say` + (`ffmpeg` or `afconvert`)
- If LLM provider is `codex`: valid OAuth auth file (`CODEX_AUTH_FILE`) or use built-in OAuth login

## Environment Variables

Core:

```bash
export GATEWAY_LISTEN_ADDR=:18910
export GATEWAY_WS_PATH=/ws
export GATEWAY_WS_TOKEN=replace-me
export GATEWAY_LLM_PROVIDER=codex
export GATEWAY_STT_PROVIDER=openai
export GATEWAY_TTS_PROVIDER=openai
export GATEWAY_MEMORY_DIR=$HOME/.gateway-memory
export GATEWAY_MEMORY_CONTEXT_SIZE=10
export GATEWAY_SESSION_SILENCE=900ms
export GATEWAY_STT_STREAMING_ENABLED=true
export GATEWAY_STT_INTERIM_INTERVAL=600ms
export GATEWAY_STT_INTERIM_MIN_AUDIO=700ms
```

`GATEWAY_MEMORY_DIR` stores SQLite memory DB at `<dir>/memory.sqlite3` by default.

Local STT/TTS (no openclaw dependency):

```bash
export GATEWAY_STT_PROVIDER=local
export GATEWAY_TTS_PROVIDER=local
export GATEWAY_MLX_WHISPER_BIN=mlx_whisper
export GATEWAY_MLX_WHISPER_MODEL=mlx-community/whisper-large-v3-turbo
export GATEWAY_MLX_WHISPER_RESIDENT_ENABLED=true
export GATEWAY_MLX_WHISPER_RESIDENT_TIMEOUT=8s
export GATEWAY_LOCAL_STT_ADDR=127.0.0.1:19610
export GATEWAY_LOCAL_TTS_ADDR=127.0.0.1:19611
export GATEWAY_LOCAL_TTS_VOICE='Daniel' # default English male voice
export GATEWAY_LOCAL_TTS_RATE=220 # words per minute for macOS `say`
export GATEWAY_LOCAL_TTS_SAMPLE_RATE=24000
export GATEWAY_LOCAL_MODULE_STARTUP_TIMEOUT=10s
```

Codex:

```bash
export CODEX_AUTH_FILE=$HOME/.opencode/auth/openai.json
export CODEX_BASE_URL=https://chatgpt.com/backend-api
export CODEX_MODEL=gpt-5.1-codex
export CODEX_SYSTEM_PROMPT='You are Alfredo, a concise voice assistant.'
```

Built-in OAuth for Codex:

```bash
export CODEX_OAUTH_CLIENT_ID=app_EMoamEEZ73f0CkXaXp7hrann
export CODEX_OAUTH_AUTHORIZE_URL=https://auth.openai.com/oauth/authorize
export CODEX_OAUTH_TOKEN_URL=https://auth.openai.com/oauth/token
export CODEX_OAUTH_REDIRECT_URI=http://localhost:1455/auth/callback
export CODEX_OAUTH_SCOPE='openid profile email offline_access'
```

OpenAI STT/TTS:

```bash
export OPENAI_API_KEY=...
export OPENAI_BASE_URL=https://api.openai.com/v1
export OPENAI_STT_MODEL=gpt-4o-mini-transcribe
export OPENAI_STT_LANGUAGE=zh
export OPENAI_TTS_MODEL=tts-1
export OPENAI_TTS_VOICE=alloy
```

Local/MLX Whisper STT language hint (recommended to avoid auto-detect drift):

```bash
export GATEWAY_STT_LANGUAGE=en   # e.g. en / zh / ja
```

OpenClaw (legacy optional, no longer required for STT/TTS):

```bash
export GATEWAY_OPENCLAW_URL=ws://127.0.0.1:18789
export GATEWAY_OPENCLAW_TOKEN=
export GATEWAY_OPENCLAW_SESSION_KEY=alfredo
export GATEWAY_OPENCLAW_AGENT_ID=
export GATEWAY_OPENCLAW_DIAL_TIMEOUT_SEC=10
export GATEWAY_OPENCLAW_ROOT=/opt/homebrew/lib/node_modules/openclaw
```

## Run

```bash
cd /Users/dev/robot/m5stack/gateway
GATEWAY_LISTEN_ADDR=:1455 \
GATEWAY_STT_PROVIDER=local \
GATEWAY_TTS_PROVIDER=local \
CODEX_OAUTH_REDIRECT_URI=http://localhost:1455/auth/callback \
go run ./cmd/codex-gateway
```

### Quick Start For `alfredo-esp32`

```bash
cd /Users/dev/robot/m5stack/gateway
./scripts/start-alfredo-gateway.sh
```

Build then run with logs (defaults: `llm=codex stt=local tts=local`):

```bash
cd /Users/dev/robot/m5stack/gateway
./scripts/build-run-gateway.sh
```

Build then run in background:

```bash
cd /Users/dev/robot/m5stack/gateway
./scripts/build-run-gateway.sh --daemon
tail -f logs/gateway-*.log
```

Run in background:

```bash
cd /Users/dev/robot/m5stack/gateway
./scripts/start-alfredo-gateway.sh --daemon
tail -f /tmp/codex-gateway.log
```

For first-time Codex OAuth:

1. Open `http://127.0.0.1:18910/`
2. Click `Sign in with GPT OAuth`
3. Complete login and return to callback
4. Verify `authorized: true` in status panel

## API Endpoints

- `GET /healthz`
- `GET /oauth2/initiate`
- `GET /oauth2/callback?code=...&state=...`
- `GET /api/oauth/status`
- `GET /api/runtime/config`
- `POST /api/runtime/config` with runtime fields like `model`, `effort`, `verbosity`, `online`, `tts_voice`, `tts_rate`
- `POST /api/runtime/config` also supports `context_messages` (default `10`)
- `POST /api/chat` with `{"text":"...","model":"gpt-5.1-codex","effort":"medium","verbosity":"medium","context_messages":10,"online":true}`
- `GET /api/memory/sessions?limit=200`
- `GET /api/memory/search?session_id=<sid>&q=<keyword>&page=1&page_size=50`
- `GET /api/memory/recent?session_id=<sid>&page=1&page_size=50`
- `POST /api/transcribe` with multipart field `audio`
- `POST /api/tts` with `{"text":"...","voice":"Daniel","rate":220}`
- `GET /api/devices` to list currently connected websocket device sessions
- `POST /api/devices/sdcard-config` with `{"session_id":"...","config":{...},"reboot":false}` to send a `system` write command

## Configure Device SD Card From Gateway UI

1. Open `http://127.0.0.1:18910/`.
2. In `Device SD Config`, click `Refresh Devices` and select the target device.
3. Edit JSON in `Runtime Config JSON`.
4. Optionally set `Reboot After Apply = Yes`.
5. Click `Apply To Device SD`.

## Point `alfredo-esp32` At The Gateway

Return this through your OTA config endpoint:

```json
{
  "websocket": {
    "url": "ws://<gateway-host>:18910/ws",
    "token": "replace-me",
    "version": 1
  }
}
```

## Verification

```bash
go test ./...
go build ./cmd/codex-gateway
```
