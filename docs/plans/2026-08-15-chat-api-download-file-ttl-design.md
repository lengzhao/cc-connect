# chat-api Download File TTL (Lazy GC)

> Version: 2026-08-15  
> Status: implemented  
> Public API: [chat-api.zh-CN.md](../chat-api.zh-CN.md)  
> Extends: [2026-08-09-chat-api-file-upload-design.md](./2026-08-09-chat-api-file-upload-design.md)

## Goal

Agent 产出的可下载文件（`{workspace}/.cc-connect/chat-api/download/`）默认只保留最近 **3 天**；更早的文件在访问该 channel 的任意文件 API 时被动删除。客户端 `uploads/` 与特权路径不受影响。无配置项。

## Rules

| Item | Value |
|------|-------|
| Scope | Managed `download/` only（`kind=download` / `SendFile`） |
| TTL | Fixed `72h` from meta `created_at`（缺省时回退 meta 文件 mtime） |
| Trigger | `GET/POST /files`、`GET /files/{id}`、`GET /files/by-path`、`SendFile` |
| Throttle | Per-channel 最短间隔 1 分钟，避免每次请求扫盘 |
| Delete | Content + `.meta.json` 一并删除；失败只打日志，不阻断主请求 |
| Expired GET | GC 先跑 → 再 `loadFile` → `404` |

## Non-goals

- Configurable TTL
- Background ticker / platform-wide sweep
- `uploads/` TTL
- Cross-replica shared GC

## Docs

- Update `docs/chat-api.zh-CN.md` / `docs/chat-api.md`
- Amend upload design non-goals: download lazy GC is now in scope
