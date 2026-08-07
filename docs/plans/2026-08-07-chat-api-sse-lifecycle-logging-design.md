# chat-api SSE 连接生命周期日志

> Date: 2026-08-07
> Status: implemented
> Related: [断链重连](./2026-07-24-chat-api-disconnect-resume-design.md)
> External contract: [docs/chat-api.zh-CN.md](../chat-api.zh-CN.md)

## Goal

为 chat-api SSE 连接打印可检索的生命周期日志（统一 **Info**，每事件一行）：

- 连接开始 / 断开挂起 / 重连挂载 / 正常结束
- 断开原因、resume 补发事件名、终态错误等字段挂在同一条 Info 上（不双写）

## Non-goals

- 不记录每条 `text_delta` / `thinking_delta` / `ping`
- 不改变对外 HTTP/SSE 契约

## Decision

采用集中 helper `logSSELifecycle`，统一前缀与字段，全部为 `slog.LevelInfo`：

```
chat-api: sse <event>
```

| event | 时机 | 可选字段 |
|-------|------|----------|
| `start` | 新消息 SSE 已发出 `message` | |
| `disconnect` | 客户端断开或写失败 → `detach`（run 挂起） | `reason=client_gone\|write_error`，`error` |
| `resume` | resume 挂载成功（补发成功后） | `replay_event` |
| `resume_miss` | resume 时 run 不存在或非归属 user，返回空 `message_end` | `reason=run_not_found\|user_mismatch` |
| `resume_rejected` | resume 时 run 仍被占用 | `reason=already_attached` |
| `end` | turn 终态（`runState.complete` 时，无论 sink 是否 Active） | `terminal`，`error` |

公共字段：`run_id`, `user`, `conversation_id`, `channel`（`resume_miss` 无 channel）  
`end.terminal` = `message_end` \| `error` \| `message_queued`

## Call sites

- `handleChatMessages`：`start`（`message` 写成功后）；`message` 写失败 → `disconnect`（`reason=write_error`）+ `detach`（不打 `start`）
- `serveRunSSE`：`disconnect`（`reqCtx.Done` / `flushDelta` 写失败）
- `handleChatResume`：`resume`（Info，可含 `replay_event`）/ `resume_miss` / `resume_rejected`
- `runState.complete`：`end` + `terminal`（含 timeout、断线后 turn 结束等）
