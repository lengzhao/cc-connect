# chat-api Privileged Files + Managed Naming Design

> Version: 2026-08-15  
> Status: implemented  
> Public API: [chat-api.zh-CN.md](../chat-api.zh-CN.md)  
> Extends: [2026-08-09-chat-api-file-upload-design.md](./2026-08-09-chat-api-file-upload-design.md)

## Goal

Two additive changes to chat-api file I/O:

1. **Managed file naming**: store content as `file_<id>.<filename>` (API id remains `file_<id>`).
2. **Privileged path mode** (opt-in config): upload/download by arbitrary host path (`./…`, `dir/…`, `~/…`, absolute), with explicit overwrite control. Path uploads are a pure bypass — no `file_id`, not listed.

## Config

| Option | Default | Description |
|--------|---------|-------------|
| `privileged_files` | `false` | When `true`, allow `path` on upload and `GET /files/by-path` |

Unset / `false`: any request with `path` or hitting `by-path` returns `403`.

## Managed naming (always on)

### Disk layout

```
{workspace}/uploads/
  file_<id>.<sanitized_filename>           ← content
  file_<id>.<sanitized_filename>.meta.json  ← meta (id still file_<id>)

{workspace}/.cc-connect/chat-api/download/
  file_<id>.<sanitized_filename>
  file_<id>.<sanitized_filename>.meta.json
```

- `GET /files/{file_id}`, list, and chat `upload_file_id` continue to use opaque `file_<id>` only.
- Locate content by prefix `file_<id>.` under the kind directory (id pattern is fixed; filename may contain dots).
- **Read compatibility**: legacy content path `file_<id>` + `file_<id>.meta.json` (no filename suffix) remains readable; new writes use the new layout only.
- Agent `SendFile` uses the same naming under `download/`.

### Filename sanitize

Strip path separators and unsafe characters; empty → `file`.

## Privileged path mode

### Path resolution

| Input | Rule |
|-------|------|
| Relative (`dir/file`, `./dir/file`) | Relative to **channel workspace** `base_dir/<channel>/`. Leading `./` optional. |
| `~/…` / `~` | Expand to process user home |
| Absolute | Use as-is |
| `..` | Allowed (may leave workspace) |

After expand: `filepath.Clean` + absolute. Empty/whitespace → `400`.

**Security note:** enabling `privileged_files` exposes host filesystem read/write to any authenticated chat-api client for that project. Document prominently.

### Upload (`POST /files`)

Multipart fields:

| Field | Required | Notes |
|-------|----------|-------|
| `file` | yes | Binary |
| `path` | privileged | Target path; absent → managed upload |
| `overwrite` | no | Default `false`; must be `true` to replace existing file |

Behavior when `path` set:

- `privileged_files=false` → `403`
- Target exists and `overwrite` is not true → `409`
- Create parent dirs as needed (`mkdir -p`)
- Write bytes to resolved absolute path
- **Do not** write under `uploads/`, **do not** allocate `file_id`, **do not** appear in `GET /files` list
- Still subject to `max_upload_size`

Response:

```json
{
  "ok": true,
  "data": {
    "path": "/abs/resolved/dir/file.txt",
    "filename": "file.txt",
    "mime_type": "text/plain",
    "size": 12,
    "created_at": 1780000000,
    "overwritten": false
  }
}
```

Managed upload response shape unchanged (`id`, `filename`, `mime_type`, `size`, `created_at`).

### Download by path

```http
GET /files/by-path?path=dir/file
X-Chat-API-Channel: ...
Authorization: Bearer ...
```

- Privilege off → `403`
- Missing / not a regular file → `404`
- Returns raw bytes; `Content-Disposition` from basename

`GET /files/{file_id}` unchanged (managed store only).

## Errors

| Case | HTTP |
|------|------|
| Privilege off + `path` / `by-path` | `403` |
| Invalid / empty path | `400` |
| Exists without `overwrite=true` | `409` |
| Path download missing / not regular file | `404` |
| Over `max_upload_size` | `413` |
| Managed path needs `base_dir` / workspace | `500` |

## Non-goals

- Privilege gated by `admin_from` (instance-wide config flag only)
- Registering path uploads into list / `local_file`
- TTL / GC / cross-replica storage
- Changing SSE `file_ready` payload shape (still `file_id` + `filename`)

## Docs / config touchpoints

- `docs/chat-api.zh-CN.md`, `docs/chat-api.md`
- `config.example.toml` (commented `privileged_files`)
- Keep this plan as source of truth alongside the 2026-08-09 upload design
