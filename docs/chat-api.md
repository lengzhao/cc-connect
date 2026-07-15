# chat-api Platform — API v1

> Version: **v1.0.0** (2026-07-09)  
> Full spec: [chat-api.zh-CN.md](./chat-api.zh-CN.md)  
> Design: [plans/2026-06-29-chat-api-platform-design.md](./plans/2026-06-29-chat-api-platform-design.md)

## Overview

`chat-api` is a cc-connect **Platform** — HTTP + SSE API for custom apps / BFFs.

**v1**: 6 endpoints, SSE-only chat, implicit conversation create, default `busy_policy=queue`.

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/conversations` | List |
| `PATCH` | `/conversations/{id}` | Rename |
| `DELETE` | `/conversations/{id}` | Delete |
| `GET` | `/conversations/{id}/messages` | History |
| `POST` | `/chat-messages` | Send (SSE) |
| `POST` | `/runs/{run_id}/cancel` | Cancel turn |

## Conventions

- REST: `{"ok": true, "data": ...}` / `{"ok": false, "error": "..."}`
- Chat: successful `POST /chat-messages` returns SSE (not JSON envelope)
- Auth: `Authorization: Bearer <api_token>` (required in production)
- User: `X-Chat-API-User` on writes; query or header on list/delete
- Optional `X-Chat-API-Channel` on send/cancel for multi-workspace `work_dir` binding
- `message_id`: `{conversation_id}:{turn_index}`
- Client disconnect does not stop the agent; use cancel endpoint to abort

## SSE events

`message`, `thinking_delta`, `tool_call`, `tool_result`, `text_delta`, `message_end`, `error`, `message_queued`

`tool_call` / `tool_result` are ephemeral (not written to history). See [Tool SSE design](./plans/2026-07-15-chat-api-tool-sse-design.md).

## Configuration

```toml
[[projects.platforms]]
type = "chat-api"

[projects.platforms.options]
listen_addr = ":8030"
path = "/v1/"
api_token = "your-service-token"
busy_policy = "queue"
```

See [chat-api.zh-CN.md](./chat-api.zh-CN.md) for full field reference and examples.
