# chat-api Platform — API v1

> Version: **v1.0.0-draft** (2026-06-29)  
> Full spec: [chat-api.zh-CN.md](./chat-api.zh-CN.md)  
> Design: [plans/2026-06-29-chat-api-platform-design.md](./plans/2026-06-29-chat-api-platform-design.md)

## Overview

`chat-api` is a cc-connect **Platform** — HTTP API for custom apps / BFFs, without Management API.

**v1**: 6 endpoints, SSE-only chat, implicit conversation create, default busy **queue**, slim JSON models.

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/conversations` | List (cursor pagination) |
| `PATCH` | `/conversations/{id}` | Rename |
| `DELETE` | `/conversations/{id}` | Delete |
| `GET` | `/conversations/{id}/messages` | History |
| `POST` | `/chat-messages` | Send (SSE) |
| `POST` | `/runs/{run_id}/cancel` | Cancel in-flight turn |

## Conventions

- Envelope: `{"ok": true, "data": ...}` / `{"ok": false, "error": "..."}`
- User: `X-Chat-API-User` on writes; query or header on reads
- `message_id`: `{conversation_id}:{turn_index}`
- Client disconnect does **not** stop the agent turn; use `POST /runs/{run_id}/cancel` to abort

## SSE events (v1)

`message`, `text_delta`, `message_end`, `error`, `message_queued`

## Not in v1

Blocking JSON mode, `POST /conversations`, empty placeholder fields (`metadata`, `retriever_resources`, etc.)

## Configuration

```toml
[[projects.platforms]]
type = "chat-api"

[projects.platforms.options]
listen_addr = ":8030"
path = "/v1/"
api_token = "your-service-token"
user_header = "X-Chat-API-User"
busy_policy = "queue"
```
