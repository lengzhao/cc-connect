# chat-api Platform — API v1

> Version: **v1.5.2** (2026-08-15)  
> Full spec: [chat-api.zh-CN.md](./chat-api.zh-CN.md)  
> Design: [plans/2026-06-29-chat-api-platform-design.md](./plans/2026-06-29-chat-api-platform-design.md)  
> File upload: [plans/2026-08-09-chat-api-file-upload-design.md](./plans/2026-08-09-chat-api-file-upload-design.md)  
> Privileged files: [plans/2026-08-15-chat-api-privileged-files-design.md](./plans/2026-08-15-chat-api-privileged-files-design.md)  
> Download file TTL: [plans/2026-08-15-chat-api-download-file-ttl-design.md](./plans/2026-08-15-chat-api-download-file-ttl-design.md)  
> Close idle agent sessions: [plans/2026-08-15-chat-api-close-idle-agent-sessions-design.md](./plans/2026-08-15-chat-api-close-idle-agent-sessions-design.md)  
> Skip prompt meta: [plans/2026-08-15-chat-api-skip-prompt-meta-design.md](./plans/2026-08-15-chat-api-skip-prompt-meta-design.md)  
> Forward headers: [plans/2026-07-21-chat-api-forward-headers-design.md](./plans/2026-07-21-chat-api-forward-headers-design.md)  
> Stream answer parse: [plans/2026-07-21-chat-api-stream-answer-parse-design.md](./plans/2026-07-21-chat-api-stream-answer-parse-design.md)  
> Structured streaming (planned): [plans/2026-07-21-structured-streaming-card-design.md](./plans/2026-07-21-structured-streaming-card-design.md)

## Overview

`chat-api` is a cc-connect **Platform** — HTTP + SSE API for custom apps / BFFs.

**v1**: SSE-only chat, implicit conversation create, default `busy_policy=queue`, tool SSE events, and permission / AskUserQuestion confirm windows.

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/conversations` | List |
| `GET` | `/conversations/{id}` | Conversation detail |
| `POST` | `/conversations/{id}/name/generate` | Generate name asynchronously |
| `PATCH` | `/conversations/{id}` | Rename |
| `DELETE` | `/conversations/{id}` | Delete |
| `GET` | `/conversations/{id}/messages` | History |
| `POST` | `/chat-messages` | Send (SSE) |
| `POST` | `/runs/{run_id}/cancel` | Cancel turn |
| `POST` | `/runs/{run_id}/interactions/{interaction_id}/respond` | Respond to confirm window |
| `GET` | `/files` | List managed files (`kind=upload\|download\|all`) |
| `POST` | `/files` | Upload (managed, or privileged `path` / `overwrite`) |
| `GET` | `/files/{file_id}` | Download managed file |
| `GET` | `/files/by-path` | Download by host path (`?path=`; needs `privileged_files`) |
| `POST` | `/agent-sessions/close-idle` | Close idle live agent processes (Engine-wide; no Channel required) |

## Conventions

- REST: `{"ok": true, "data": ...}` / `{"ok": false, "error": "..."}`
- Chat: successful `POST /chat-messages` returns SSE (not JSON envelope)
- Auth: `Authorization: Bearer <api_token>` (required in production)
- User: `X-Chat-API-User` on writes; query or header on list/delete
- Optional `X-Chat-API-Channel` on send for multi-workspace `work_dir` binding (omit → `default_channel`; cancel/respond reuse run channel)
- Optional `X-Chat-API-Skip-Prompt-Meta: true` on `POST /chat-messages` to skip Engine `[cc-connect ...]` injection for that turn (`true`/`1`/`yes`)
- **Exception:** `POST /agent-sessions/close-idle` does **not** require `X-Chat-API-Channel` (Engine-wide op; Channel/User headers optional for audit logs only)
- `auto_generate_name` applies to newly created conversations; `auto_generate_name_mode` defaults to `heuristic` and may be set to `ai`
- In `ai` mode, `name_model` selects a separate low-cost model while credentials and endpoint are reused from `name_provider` or the configured project provider; name generation never calls the main Agent and falls back to heuristic naming when no provider is available
- `message_id`: `{conversation_id}:{turn_index}`
- Client disconnect does not stop the agent; use cancel endpoint to abort

## Close idle agent sessions

`POST /agent-sessions/close-idle` closes **idle** live agent processes across the project Engine (including other IM platforms on the same project). Busy / permission-waiting / queued sessions are skipped. Session metadata and `agent_session_id` are preserved so the next message can resume.

Success `200` data fields: `closed`, `skipped`, `closed_session_keys`, `skipped_session_keys`. Empty idle set still returns `200` with `closed=0`. Unbound closer → `503`.

See [close-idle design](./plans/2026-08-15-chat-api-close-idle-agent-sessions-design.md).

## Files

- Managed store uses disk names `file_<id>.<filename>` (API id remains `file_<id>`; legacy `file_<id>` still readable)
- Agent `download/` files are retained for **72 hours**; older ones are deleted lazily when any file API touches that channel (`GET/POST /files`, `GET /files/{id}`, `GET /files/by-path`, `SendFile`); per-channel scan throttle 1 minute. `uploads/` and privileged paths are unaffected
- Opt-in `privileged_files` (default `false`): multipart `path` / `overwrite` on `POST /files`, and `GET /files/by-path?path=`
- **Security:** when enabled, authenticated clients can read/write host paths (relative to channel workspace; `./` optional; `~/` and absolute allowed)
- Path uploads do **not** get a `file_id` and are **not** listed in `GET /files`; `overwrite` defaults to `false` (need `true` to replace)

## SSE events

`message`, `thinking_delta`, `tool_call`, `tool_result`, `text_delta`, `file_ready`, `permission_request`, `question_request`, `interaction_superseded`, `interaction_ack`, `ping`, `message_end`, `error`, `message_queued`

`text_delta` / `thinking_delta` may include optional `replace: true` (full-text replace instead of append). Clients must handle it or progress lines can duplicate into the answer.

`tool_call` / `tool_result` / interaction events are ephemeral (not written to history). Agent file output uses `file_ready` + `GET /files/{id}`. See [Tool SSE design](./plans/2026-07-15-chat-api-tool-sse-design.md) and [Interaction hardening](./plans/2026-07-14-chat-api-interaction-hardening.md).

`question_request` includes `multi_select` (`true`/`false`). Clients must use `option_id` for single-select and `option_ids` only when `multi_select=true`.

## Configuration

```toml
[[projects.platforms]]
type = "chat-api"

[projects.platforms.options]
listen_addr = ":8030"
path = "/v1/"
api_token = "your-service-token"
busy_policy = "queue"
auto_generate_name_mode = "heuristic"
name_provider = "" # optional; empty uses project agent provider
name_model = "gpt-4o-mini"
name_provider_type = "openai" # openai | openai-compatible | claude
interaction_timeout = "10m"
sse_ping_interval = "15s"
# max_upload_size = "50MiB"
# privileged_files = false  # WARNING: host FS read/write when true
# forward_headers = ["X-Tenant-Id", "X-Trace-Id"]  # hooks only (CC_HOOK_HEADERS_JSON); not agent prompt
```

See [chat-api.zh-CN.md](./chat-api.zh-CN.md) for full field reference and examples.
