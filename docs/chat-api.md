# chat-api Platform — API v1

> Version: **v1.1.2** (2026-07-15)  
> Full spec: [chat-api.zh-CN.md](./chat-api.zh-CN.md)  
> Design: [plans/2026-06-29-chat-api-platform-design.md](./plans/2026-06-29-chat-api-platform-design.md)

## Overview

`chat-api` is a cc-connect **Platform** — HTTP + SSE API for custom apps / BFFs.

**v1**: SSE-only chat, implicit conversation create, default `busy_policy=queue`, tool SSE events, and permission / AskUserQuestion confirm windows.

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/conversations` | List |
| `PATCH` | `/conversations/{id}` | Rename |
| `DELETE` | `/conversations/{id}` | Delete |
| `GET` | `/conversations/{id}/messages` | History |
| `POST` | `/chat-messages` | Send (SSE) |
| `POST` | `/runs/{run_id}/cancel` | Cancel turn |
| `POST` | `/runs/{run_id}/interactions/{interaction_id}/respond` | Respond to confirm window |

## Conventions

- REST: `{"ok": true, "data": ...}` / `{"ok": false, "error": "..."}`
- Chat: successful `POST /chat-messages` returns SSE (not JSON envelope)
- Auth: `Authorization: Bearer <api_token>` (required in production)
- User: `X-Chat-API-User` on writes; query or header on list/delete
- Optional `X-Chat-API-Channel` on send for multi-workspace `work_dir` binding (omit → `default_channel`; cancel/respond reuse run channel)
- `message_id`: `{conversation_id}:{turn_index}`
- Client disconnect does not stop the agent; use cancel endpoint to abort

## SSE events

`message`, `thinking_delta`, `tool_call`, `tool_result`, `text_delta`, `permission_request`, `question_request`, `interaction_superseded`, `interaction_ack`, `ping`, `message_end`, `error`, `message_queued`

`tool_call` / `tool_result` / interaction events are ephemeral (not written to history). See [Tool SSE design](./plans/2026-07-15-chat-api-tool-sse-design.md) and [Interaction hardening](./plans/2026-07-14-chat-api-interaction-hardening.md).

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
interaction_timeout = "10m"
sse_ping_interval = "15s"
```

See [chat-api.zh-CN.md](./chat-api.zh-CN.md) for full field reference and examples.
