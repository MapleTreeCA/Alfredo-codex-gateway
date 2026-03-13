# Gateway Agent Guide

## Scope

This repo is the voice gateway for `alfredo-esp32`.
Default focus is:

- WebSocket device session flow
- HTTP/web console
- Codex OAuth and runtime config
- STT/TTS provider wiring
- memory/session persistence

Do not read the whole repo by default. Start from the smallest path that matches the task.

## Start Here

Read these first for most tasks:

1. `README.md`
2. `cmd/codex-gateway/main.go`
3. `internal/config/config.go`
4. `internal/server/server.go`
5. `internal/server/session.go`
6. `internal/server/session_pipeline.go`
7. `internal/server/http_handlers.go`
8. `internal/server/runtime_config.go`

## Read By Task

Pick only the relevant area:

- HTTP/API/UI task:
  `internal/server/http_handlers.go`, `internal/server/web.go`, `internal/server/web/*`
- WebSocket/device/session task:
  `internal/server/session.go`, `internal/server/session_pipeline.go`, `internal/server/server.go`
- runtime config or env var task:
  `internal/config/config.go`, `internal/server/runtime_config.go`, `README.md`
- memory/history task:
  `internal/memorystore/store.go`, `internal/server/memory_handlers.go`
- audio encode/decode task:
  `internal/audio/*`
- Codex OAuth/auth task:
  `internal/codexauth/*`
- OpenAI STT/TTS task:
  `internal/openai/*`
- local STT/TTS or MLX Whisper task:
  `internal/local/*`, `internal/localmodule/*`, `internal/localtts/*`, `internal/mlxwhisper/*`
- OpenClaw legacy task:
  `internal/openclaw/*`
- startup/run script task:
  `scripts/start-alfredo-gateway.sh`, `scripts/build-run-gateway.sh`

## Tests

When changing a package, read the nearest `*_test.go` files in the same area before editing.
For server/session changes, start with:

- `internal/server/session_pipeline_test.go`
- `internal/server/session_streaming_test.go`
- `internal/server/session_voice_gate_test.go`
- `internal/server/runtime_config_test.go`

## Usually Skip

Ignore these unless the user explicitly asks for them:

- `codex-gateway`
- `bin/`
- `.tmp/`
- `logs/`
- `.git/`
- `dev/*.md`
- `doc/*.md`
- provider packages unrelated to the current task

## Working Rules

- Use `rg` on route names, env vars, protocol fields, and symbols before opening files.
- Do not read every provider implementation. Choose the active provider path first.
- If the task is about `alfredo-esp32` interop, read only the relevant protocol/config files on the firmware side instead of scanning the whole firmware repo.
