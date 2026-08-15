# chat-api File Upload / Download Design

> Version: 2026-08-09 (rev 2)  
> Status: implemented in `platform/chat-api`  
> Public API: [chat-api.zh-CN.md](../chat-api.zh-CN.md)  
> Follow-up: [2026-08-15-chat-api-privileged-files-design.md](./2026-08-15-chat-api-privileged-files-design.md) (`file_<id>.<filename>` naming + optional `privileged_files`)

## Goal

Workspace-scoped file I/O for chat-api:

- **Client upload** → `{workspace}/uploads/` — agent reads paths directly
- **Agent output** (`core.FileSender`) → `{workspace}/.cc-connect/chat-api/download/` — client downloads via API + SSE `file_ready`

All file endpoints require `X-Chat-API-Channel` (same as chat-messages).

## Workspace layout

```
{base_dir}/{channel}/          ← workspace (Engine work_dir)
  uploads/
    file_<id>.<filename>                  ← client upload content
    file_<id>.<filename>.meta.json
  .cc-connect/chat-api/download/
    file_<id>.<filename>                  ← agent SendFile content
    file_<id>.<filename>.meta.json
```

API / chat still use opaque `file_<id>`. Legacy on-disk `file_<id>` (+ `.meta.json`) remains readable. Naming details: [2026-08-15 privileged files design](./2026-08-15-chat-api-privileged-files-design.md).

Requires project `base_dir` / platform `cc_base_dir` (multi-workspace). Without it, file APIs return `500`.

## Endpoints

| Method | Path | Headers | Description |
|--------|------|---------|-------------|
| `GET` | `/v1/files` | `X-Chat-API-Channel` | List files (`?kind=upload\|download\|all`, cursor pagination) |
| `POST` | `/v1/files` | `X-Chat-API-User`, `X-Chat-API-Channel` | Multipart upload → `uploads/` |
| `GET` | `/v1/files/{file_id}` | `X-Chat-API-Channel` | Download from `uploads/` or `download/` |

## Chat input reference

```json
{
  "type": "file",
  "transfer_method": "local_file",
  "upload_file_id": "file_abc..."
}
```

For `type=file`, chat-api appends the absolute `uploads/` path to the prompt via `core.AppendFileRefs` — **no copy** to `.cc-connect/attachments/`. Images/audio still read bytes from disk when needed.

## Agent output (FileSender)

`Platform.SendFile` writes to `.cc-connect/chat-api/download/` and emits SSE:

```text
event: file_ready
data: {"message_id":"...","file_id":"file_...","filename":"...","mime_type":"...","size":123}
```

Client downloads with `GET /files/{file_id}` + same channel header.

## Limits

| Option | Default |
|--------|---------|
| `max_upload_size` | `50MiB` |

## Non-goals (v1)

- Upload TTL / GC（`uploads/` 仍不自动清理）
- Cross-replica shared storage

> Download lazy GC（2026-08-15）：Agent 产出目录 `.cc-connect/chat-api/download/` 固定保留 72h，在任意文件 API 触达该 channel 时被动删除过期文件。详见 [2026-08-15-chat-api-download-file-ttl-design.md](./2026-08-15-chat-api-download-file-ttl-design.md)。
