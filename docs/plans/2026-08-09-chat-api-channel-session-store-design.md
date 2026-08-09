# chat-api Channel Session Store Design

> Date: 2026-08-09  
> Status: approved — implementation in progress

## Goal

Replace monolithic `sessions/{project}.json` snapshot storage for chat-api multi-replica
deployments with **flat, channel-sharded, JSONL append-only** persistence aligned with
IM platforms (session scoped by channel / chat, not end-user).

## Non-goals

- No conversation **delete** API or storage events
- No URL path changes (`/v1/conversations` stays)
- No `{channel}` path segment
- Does not fix SSE `pendingStore` cross-replica sharing (separate concern)

## Session model (aligned with IM)

| Concept | Value |
|---------|--------|
| SessionManager userKey | `chat-api:{channel}` |
| Engine interactive key | `chat-api:{channel}:{conversation_id}` (unchanged) |
| List scope | all conversations under channel |
| `user` header | sender identity on writes; optional metadata on history |
| Channel header | **required on every API call** (`X-Chat-API-Channel`) |

IM analogy: Feishu `feishu:{chatID}`, chat-api `chat-api:{channel}`.

## Storage layout

```text
{session_store_dir}/
└── channels/
    └── {hash_prefix}/          # sha256(channel_key)[:2]
        ├── {hash}.jsonl        # append-only events
        └── {hash}.meta.json    # {"channel_key":"chat-api:..."} (written once)
```

Default `session_store_dir`: `~/.cc-connect/chat-api/records` (automatic when the project includes `chat-api`; no config required).

## JSONL events (v1)

No delete op.

| op | fields |
|----|--------|
| `conv_create` | `conv_id`, `name`, `created_by?`, `created_at` |
| `conv_rename` | `conv_id`, `name` |
| `conv_meta` | `conv_id`, `agent_session_id`, `agent_type`, `active_provider`, `past_agent_session_ids?` |
| `history_append` | `conv_id`, `entry` (HistoryEntry) |

Fold rules: last rename/meta wins; tombstones not used; deleted convs not supported.

## SessionStore integration (core)

When a project registers `type = "chat-api"` and `session_store` is omitted:

- `session_store = "jsonl_channel"` (implicit)
- `session_store_dir = ~/.cc-connect/chat-api/records`
- `session_store_key_prefix = "chat-api:"`

Optional overrides:

```toml
[[projects]]
name = "sit_communication_agent"
# session_store = "json"              # opt out of JSONL default
# session_store_dir = "/data/records" # override default dir
# session_store_key_prefix = "chat-api:"
```

- `json`: monolithic JSON snapshot at `sessions/{project}.json` only
- `jsonl_channel`: keys matching `session_store_key_prefix` persist via JSONL append;
  other keys still use the JSON snapshot file (mixed IM + chat-api projects)

`SessionManager.Save()` emits deltas (new conv, new history lines, meta/rename changes)
instead of rewriting the full snapshot for prefixed keys.

## API behavior (paths unchanged)

| Endpoint | Channel | User |
|----------|---------|------|
| `GET /conversations` | required | not required |
| `GET/PATCH /conversations/{id}` | required | PATCH: required |
| `GET …/messages` | required | not required |
| `POST /chat-messages` | required | required |
| cancel / interaction respond | required (or run-stored channel) | required |
| `DELETE /conversations/{id}` | **removed** | — |

Missing channel → `400 channel required`.

List returns conversations in the requested channel sorted by `updated_at`.

Conv access checks: `conv_id` must appear in `ListSessions("chat-api:"+channel)`.

## Multi-pod concurrency

- Same channel: multiple pods append to one `.jsonl` with `O_APPEND` (POSIX-safe)
- Cross-pod read: in-memory fold + tail on access (eventual consistency)
- Same conv concurrent turns: still needs sticky routing or distributed lock (out of scope)

## Migration

1. Run with single writer or maintenance window
2. Tool reads legacy `sessions/*.json`, maps chat-api sessions to channel keys
3. Emits JSONL events per channel shard
4. Enable `session_store = "jsonl_channel"`

Legacy monolithic file kept as backup; not deleted automatically.

## Testing

- JSONL append + fold unit tests
- Concurrent append test (two goroutines, one file)
- chat-api: list/messages/chat require channel header
- chat-api: sessions registered under `chat-api:{channel}`
- Regression: history preserved after message turn

## References

- [chat-api.zh-CN.md](../chat-api.zh-CN.md)
- [2026-06-29-chat-api-platform-design.md](./2026-06-29-chat-api-platform-design.md)
