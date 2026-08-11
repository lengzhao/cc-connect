# chat-api HTTP 请求/响应访问日志

> Date: 2026-08-11
> Status: implemented
> Related: [SSE 生命周期日志](./2026-08-07-chat-api-sse-lifecycle-logging-design.md)

## Goal

为 chat-api 全部 v1 HTTP API 打印请求与响应日志，便于排查时延与错误。

## Non-goals

- 不记录 SSE 流内每条 `text_delta` / `ping`（已有 SSE lifecycle 日志）
- 不改变对外 HTTP/SSE 契约
- 不提供开关；始终启用（Info 级别）

## Decision

在 `routes()` 最外层 middleware `loggingHTTP` 统一拦截：

```
chat-api: http request
chat-api: http response
```

| 日志 | 字段 |
|------|------|
| request | `request_id`, `method`, `path`, `query`, `remote_addr`, `content_length`, `user?`, `channel?`, `trace_id?`, `task?`, `body` |
| response | `request_id`, `method`, `path`, `status`, `duration_ms`, `trace_id?` |

- `request_id`：递增序号（36 进制），关联同一次请求的两条日志
- `body`：仅 request 记录，最多 300 字节，超出追加 `...(truncated)`；response 不记录 body
- 日志 key 固定为 `user` / `channel` / `trace_id` / `task`；值从配置的 `user_header`、`channel_header`、`agent_context_headers.trace_id|task_id` 对应 HTTP header 读取（仅非空时输出）；`trace_id` 在 response 日志中同样输出
- 不记录完整 headers
- SSE 长连接：`duration_ms` 覆盖 handler 全程（含整段流式输出）

## Scope

- 覆盖 `/v1/` 下 REST 与 SSE 端点（经 `wrap()` 注册的路由）
- `/debug/` 调试页不在此范围
