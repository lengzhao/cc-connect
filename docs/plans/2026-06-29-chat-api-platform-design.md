# chat-api Platform Design

> Version: 2026-06-29 draft  
> Status: API v1 defined — implemented in `platform/chat-api`  
> Public API: [chat-api.zh-CN.md](../chat-api.zh-CN.md)

## Goal

`platform/chat-api` exposes a slim HTTP + SSE client API for custom apps and BFFs, without opening Management API.

## Architecture

```mermaid
flowchart LR
  Client[App / BFF]
  API["platform/chat-api"]
  Engine[core.Engine]
  Agent[Coding Agent]

  Client -->|REST + SSE| API
  API -->|core.Message| Engine
  Engine --> Agent
  Engine -->|StreamingCard| API
  API -->|SSE| Client
```

Implements `core.Platform`, `StreamingCardPlatform`, `HookContextProvider` — same in-process pending pattern as `platform/a2a`. Does **not** use Bridge WebSocket.

## Session mapping

| API | cc-connect |
|-----|------------|
| `user`（创建者 / 发送者） | 列表归属 `chat-api:{user}`；`Message.UserID` |
| `user_name`（可选 header） | `Message.UserName` + 历史 `HistoryEntry` |
| `conversation_id` | `Session.ID`；Engine `session_key = chat-api:conv:{id}` |
| `message_id` | `{conversation_id}:{turn_index}` |
| History | 相邻 `user` + `assistant` 配对 |

- **List**: `ListSessions("chat-api:{owner}")` — 仅创建者可见
- **Participate**: `FindByID(conversation_id)` — 任意知情 user 可发消息 / 读历史
- **Create**: 隐式 `NewSession(chat-api:{creator})` on first message
- **Persist**: Engine `sessions.json`

## Busy session policy

Default `busy_policy = "queue"` — delegate to `Engine.handleMessage` → `queueMessageForBusySession`.

- Engine queues metadata; sends to **same** `agentSession` after current turn `EventResult` (no mid-turn stdin injection).
- chat-api responds with SSE `message_queued` and closes the HTTP stream.
- Optional `busy_policy = "reject"` → `409` without queueing.

Do **not** duplicate TryLock logic in chat-api; let Engine own queue semantics.

## Streaming (v1)

- Normative SSE: `message`, `text_delta`, `message_end`, `error`, `message_queued`
- `StreamingCard.Update` → `text_delta`; `Finalize` → `message_end`
- `pendingStore` per run (from `platform/a2a`)
- Client disconnect → detach SSE only; agent turn continues; partial text still saved to history

**Deferred to v1.1**: `response_mode=blocking`, optional SSE extensions (`tool_call_*`, `agent_thought`, business `metadata` events).

## Authentication

- `Authorization: Bearer <api_token>` when configured
- End-user `user` only via `user_header` on writes; reads allow query `user=` OR header
- Optional display name via `user_name_header` (default `X-Chat-API-User-Name`); persisted per user history entry as `user_id` / `user_name`
- Platform does not verify user ownership — BFF must authenticate first

## HTTP surface (v1)

| Method | Path |
|--------|------|
| `GET` | `/v1/conversations` |
| `PATCH` | `/v1/conversations/{id}` |
| `DELETE` | `/v1/conversations/{id}` |
| `GET` | `/v1/conversations/{id}/messages` |
| `POST` | `/v1/chat-messages` |
| `POST` | `/v1/runs/{run_id}/cancel` |

Response envelope: `{ok, data}` / `{ok, error}` (Bridge style).

Pagination: unified `cursor` + `next_cursor` + `has_more`.

**Disconnect vs cancel**: SSE disconnect detaches the stream only; the agent turn keeps running. Use `POST /v1/runs/{run_id}/cancel` for user-initiated abort (`Engine.StopInteractiveTurn`).

## Building blocks

### Reuse

| Need | Source |
|------|--------|
| Session CRUD | `SessionManager` |
| Dispatch + queue | `Engine.handleMessage` |
| Attachments | `core.Message` fields |
| Streaming | `platform/a2a` StreamingCard + pendingStore |
| Hooks | `HookContextProvider` |
| HTTP auth / CORS | Bridge / Management middleware patterns |

### Build in `platform/chat-api`

| Need | Notes |
|------|-------|
| Cursor pagination | Over sorted sessions / paired messages |
| query/answer pairing | View layer over `GetHistory` |
| SSE writer | Maps StreamingCard to HTTP |
| Ownership checks | list/rename/delete: owner only; messages: `FindByID` |

## v1 limitations

| Item | Behavior |
|------|----------|
| Conversation / message `metadata` | Not in API responses; hooks-only on send |
| Historical attachments | Not replayed |
| `auto_generate_name` | Truncate first `query` (32 runes), no LLM |
| Blocking JSON | BFF aggregates SSE or wait for v1.1 |

## Package layout

```
platform/chat-api/
  chatapi.go
  conversations.go
  messages.go
  chat.go
  auth.go
  sse.go
  chatapi_test.go
cmd/cc-connect/plugin_platform_chatapi.go
```

## Registry

```go
core.RegisterPlatform("chat-api", New)
```

Add to `ALL_PLATFORMS` in Makefile and `config.example.toml`.

## Testing

- Cursor pagination, message_id stability, SSE framing
- Implicit conversation create
- `message_queued` when busy + `busy_policy=queue`
- `409` when `busy_policy=reject`
- Optional CUJ: two conversations, history preserved after switch

## vs Bridge / Management

| Capability | Bridge | Management | chat-api v1 |
|------------|--------|------------|-------------|
| Session list | per `session_key` | all sessions | per `user` |
| Stream | WebSocket | — | SSE |
| Send | WS / adapter | async `/send` | SSE on same POST |
| Busy | queue (IM) | — | queue (default) |
| Public-safe | adapter model | admin | yes |
