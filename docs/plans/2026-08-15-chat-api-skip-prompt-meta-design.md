# chat-api Skip Prompt Meta Design

Date: 2026-08-15  
Status: implemented

## Goal

Allow `POST /chat-messages` clients to opt out of the Engine `[cc-connect ...]`
prefix (sender / timestamp / inject_context) for a single turn, so the agent
receives the raw query (plus any file-path appends).

## API

| Header | Values | Effect |
|--------|--------|--------|
| `X-Chat-API-Skip-Prompt-Meta` | `true` / `1` / `yes` (case-insensitive) | Skip `[cc-connect ...]` for this turn |
| omitted / other | — | Existing project `inject_*` behavior |

Scope: `POST /chat-messages` only. Cancel / interaction / resume ignore it.

Unaffected: `AppendFileRefs`, body `metadata` → hooks, `forward_headers` → hooks,
history (still stores raw query).

CORS: header is always listed in `Access-Control-Allow-Headers`.

## Core

```go
// Message.SkipPromptMeta — per-turn; not persisted
```

`queuedMessage` carries the same flag so busy-session drains keep the turn's choice.

`buildAgentPrompt` is skipped when the flag is set (call sites return `content` as-is).

## Non-goals

- Body field alternative
- Fine-grained per-attr toggles
- Changing project-level `inject_*` defaults
