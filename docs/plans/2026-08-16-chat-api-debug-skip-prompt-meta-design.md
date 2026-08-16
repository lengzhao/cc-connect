# chat-api Debug UI Skip Prompt Meta Toggle

Date: 2026-08-16  
Status: implemented

## Goal

Let the embedded `/debug/` console opt out of Engine `[cc-connect ...]` prompt
metadata per the existing `X-Chat-API-Skip-Prompt-Meta` header, without changing
the API surface.

## UI

- Config panel checkbox: **忽略 Prompt Meta** (`id="skipPromptMeta"`)
- Default: unchecked (normal `inject_*` behavior)
- Persisted in `localStorage` key `chat-api-debug-v1` via existing save/load
- **Files** / **AgentContext headers** `<details>` collapsed by default
- Default empty: `X-Task-ID`, `X-Tenant-ID`（以及已有的 `X-Trace-ID`）；`X-Language` 仍默认 `zh`

## Behavior

When checked, `POST /chat-messages` includes:

```http
X-Chat-API-Skip-Prompt-Meta: true
```

When unchecked, the header is omitted. Other endpoints unchanged.

## Non-goals

- Backend / Engine changes
- Body-field alternative
- Fine-grained per-attr toggles

## Related

- [Skip Prompt Meta API](./2026-08-15-chat-api-skip-prompt-meta-design.md)
