# chat-api Platform — API v1

> Version: **v1.2.8** (2026-07-26)
> Full spec: [chat-api.zh-CN.md](./chat-api.zh-CN.md)  
> Design: [plans/2026-06-29-chat-api-platform-design.md](./plans/2026-06-29-chat-api-platform-design.md)  
> Disconnect resume: [plans/2026-07-24-chat-api-disconnect-resume-design.md](./plans/2026-07-24-chat-api-disconnect-resume-design.md)
> AskUserQuestion card contract: [plans/2026-07-22-askuserquestion-rich-confirm-design.md](./plans/2026-07-22-askuserquestion-rich-confirm-design.md)
> Ask User MCP (Claude Code source): [plans/2026-07-23-cc-connect-ask-user-mcp-design.md](./plans/2026-07-23-cc-connect-ask-user-mcp-design.md)
> Client flow MCP: [plans/2026-07-23-chat-api-client-flow-design.md](./plans/2026-07-23-chat-api-client-flow-design.md)
> AskUserQuestion history: [plans/2026-07-23-chat-api-askuserquestion-history-design.md](./plans/2026-07-23-chat-api-askuserquestion-history-design.md)
> Forward headers: [plans/2026-07-21-chat-api-forward-headers-design.md](./plans/2026-07-21-chat-api-forward-headers-design.md)  
> Stream answer parse: [plans/2026-07-21-chat-api-stream-answer-parse-design.md](./plans/2026-07-21-chat-api-stream-answer-parse-design.md)  
> Structured streaming (planned): [plans/2026-07-21-structured-streaming-card-design.md](./plans/2026-07-21-structured-streaming-card-design.md)

## Overview

`chat-api` is a cc-connect **Platform** — HTTP + SSE API for custom apps / BFFs.

**v1**: SSE-only chat, implicit or explicit conversation create, default `busy_policy=queue`, tool SSE events, permission / AskUserQuestion confirm windows, minimal non-blocking `client_flow` guides, and SSE disconnect resume via `POST /chat-messages` with `run_id`.

> Agent-side source: Claude Code defaults to resident MCP tool `cc_connect_ask_user` so `event` / `value` / `tag` / `allow_custom_input` survive; see [Ask User MCP](./plans/2026-07-23-cc-connect-ask-user-mcp-design.md).

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/conversations` | Create empty conversation |
| `GET` | `/conversations` | List |
| `GET` | `/conversations/{id}` | Conversation detail |
| `POST` | `/conversations/{id}/name/generate` | Generate name asynchronously |
| `PATCH` | `/conversations/{id}` | Rename |
| `DELETE` | `/conversations/{id}` | Delete |
| `GET` | `/conversations/{id}/messages` | History |
| `POST` | `/chat-messages` | Send or resume SSE (`query` or `run_id`) |
| `POST` | `/runs/{run_id}/cancel` | Cancel turn |
| `POST` | `/conversations/messages/respond` | Respond to confirm (`answers[]` or `decision`) |

## Conventions

- REST: `{"ok": true, "data": ...}` / `{"ok": false, "error": "..."}`
- Chat: successful `POST /chat-messages` returns SSE (not JSON envelope)
- Auth: `Authorization: Bearer <api_token>` (required in production)
- User: `X-Chat-API-User` on writes; query or header on list/delete
- Optional `X-Chat-API-Channel` on send for multi-workspace `work_dir` binding (omit → `default_channel`; cancel/respond reuse run channel)
- `auto_generate_name` applies to newly created conversations; `auto_generate_name_mode` defaults to `heuristic` and may be set to `ai`
- In `ai` mode, `name_model` selects a separate low-cost model while credentials and endpoint are reused from `name_provider` or the configured project provider; name generation never calls the main Agent and falls back to heuristic naming when no provider is available
- `message_id`: `{conversation_id}:{turn_index}`
- Client disconnect does not stop the agent; use cancel endpoint to abort
- Resume: `POST /chat-messages` with `{"run_id":"..."}` replays the last recoverable event while the turn is still running; if the run is missing, not owned by the user, or already finished, resume returns `404 not found` (use history)
- Optional `question_notify_url` receives async webhook when `question_request` arrives while detached

## SSE events

`message`, `thinking_delta`, `tool_call`, `tool_result`, `text_delta`, `permission_request`, `question_request`, `client_flow`, `interaction_superseded`, `interaction_ack`, `ping`, `message_end`, `error`, `message_queued`

`text_delta` / `thinking_delta` may include optional `replace: true` (full-text replace instead of append). Clients must handle it or progress lines can duplicate into the answer.

`tool_call` / `tool_result` / interaction SSE events are ephemeral by default. See [Tool SSE design](./plans/2026-07-15-chat-api-tool-sse-design.md) and [Interaction hardening](./plans/2026-07-14-chat-api-interaction-hardening.md).

`question_request` uses the card contract: envelope (`interaction_id` / `run_id` / `message_id` / `expires_at` / optional `event`) + `card_group` (length 1 from Engine/Claude). Options use `label` / `value` / `description` / `tag`; custom input is `others.custom_input.enabled`. Respond only via `POST /conversations/messages/respond` — questions use `answers[]` (`value` or `custom_input`); permissions use `decision`.

`client_flow` is emitted from the independent `cc_connect_client_flow` MCP tool and carries exactly `flow_id`, `type`, `description`, `run_id`, and `message_id`. `type` is one of `connect_account`, `create_task`, `task_generating`, or `task_center_approval`, sharing the same enum and semantics as `question_request.event`. It is non-blocking: it creates no interaction, occupies no interaction slot, requires no respond, and may coexist with `question_request`. The App handles the enum locally while the SSE turn keeps streaming. See [Client flow MCP](./plans/2026-07-23-chat-api-client-flow-design.md).

After a successful question respond, chat-api always persists the confirm as normal history turns: readable question text as `assistant`, the user-visible option label (or custom input) as `user`, then the agent’s final reply. `GET …/messages` still returns plain `query`/`answer` pairs. Permission confirms are not written to history.

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
# question_notify_url = "https://bff.example.com/cc-connect/question-notify"
# question_notify_secret = "optional-shared-secret"
# question_notify_timeout = "5s"
# forward_headers = ["X-Tenant-Id", "X-Trace-Id"]  # hooks only (CC_HOOK_HEADERS_JSON); not agent prompt
```

See [chat-api.zh-CN.md](./chat-api.zh-CN.md) for full field reference and examples.
