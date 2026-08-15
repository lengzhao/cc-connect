# chat-api Privileged Files + Managed Naming Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Store managed files as `file_<id>.<filename>`, and add opt-in `privileged_files` for path-based upload/download (bypass, no `file_id`).

**Architecture:** Extend `platform/chat-api` file handlers. Managed I/O keeps opaque `file_<id>` in API/meta; disk content uses `file_<id>.<sanitized_name>`. Privileged mode resolves `./` / relative / `~/` / absolute paths (relative → channel workspace), writes/reads host paths without registering in the managed store. Route `GET …/files/by-path` before generic `{file_id}` handling.

**Tech Stack:** Go, `net/http`, existing chat-api multipart/SSE file code, `slog`.

**Design:** [2026-08-15-chat-api-privileged-files-design.md](./2026-08-15-chat-api-privileged-files-design.md)

---

### Task 1: Managed disk naming helpers + unit tests

**Files:**
- Modify: `platform/chat-api/files.go`
- Modify: `platform/chat-api/files_test.go`

**Step 1: Write the failing tests**

Add tests for:

```go
func TestManagedContentBaseName(t *testing.T) {
	// id=file_abcdefghijklmnopqrstuv, filename=report.pdf
	// → file_abcdefghijklmnopqrstuv.report.pdf
}

func TestFindManagedFilePaths_NewAndLegacy(t *testing.T) {
	dir := t.TempDir()
	id := "file_abcdefghijklmnopqrstuv"
	// write legacy: id + id.meta.json
	// find → legacy paths
	// write new: id.notes.txt + id.notes.txt.meta.json
	// find → new paths
}
```

Also assert empty sanitized name falls back to `"file"` in content basename.

**Step 2: Run tests to verify they fail**

```bash
go test ./platform/chat-api/ -run 'TestManagedContentBaseName|TestFindManagedFilePaths' -count=1 -v
```

Expected: FAIL (undefined helpers).

**Step 3: Minimal implementation**

In `files.go`:

```go
func managedContentBaseName(id, filename string) string {
	name := sanitizeUploadFilename(filename)
	if name == "" {
		name = "file"
	}
	return id + "." + name
}

// findManagedFilePaths looks up content+meta for fileID under dir.
// Prefer new layout file_<id>.<name>[.meta.json]; fall back to legacy file_<id>.
func findManagedFilePaths(dir, fileID string) (contentPath, metaPath string, err error)
```

Update `writeFileRecord` to write:

- `contentPath = filepath.Join(dir, managedContentBaseName(id, filename))`
- `metaPath = contentPath + uploadMetaSuffix`

Update `readFileRecord` / `uploadedFilePath` to use `findManagedFilePaths` (return real content path, not `Join(dir, fileID)` only).

Update `listFileMetasInDir`: when `meta.ID == ""`, derive id with `fileIDPattern` prefix from the meta filename (do **not** use full `file_<id>.name` as id). Prefer relying on written `meta.ID`.

**Step 4: Run tests**

```bash
go test ./platform/chat-api/ -run 'TestManagedContentBaseName|TestFindManagedFilePaths|TestListFiles|TestUploadAndDownloadFile' -count=1
```

Expected: PASS (update any assertions that `Stat` `Join(base, channel, uploads, id)` to glob/prefix or use helper).

**Step 5: Commit**

```bash
git add platform/chat-api/files.go platform/chat-api/files_test.go
git commit -m "feat(chat-api): store managed files as file_<id>.<filename>"
```

---

### Task 2: Config `privileged_files`

**Files:**
- Modify: `platform/chat-api/chatapi.go`
- Modify: `platform/chat-api/chatapi_test.go` (or existing New() tests)

**Step 1: Failing test**

```go
func TestNew_PrivilegedFilesOption(t *testing.T) {
	p, err := New(map[string]any{"privileged_files": true, "api_token": "t"})
	// assert p.(*Platform).privilegedFiles == true
	p2, _ := New(map[string]any{"api_token": "t"})
	// default false
}
```

**Step 2: Run — expect fail**

```bash
go test ./platform/chat-api/ -run TestNew_PrivilegedFilesOption -count=1 -v
```

**Step 3: Implement**

Add `privilegedFiles bool` on `Platform`; set via `boolOption(opts, "privileged_files", false)`.

**Step 4: Pass + commit**

```bash
go test ./platform/chat-api/ -run TestNew_PrivilegedFilesOption -count=1
git add platform/chat-api/chatapi.go platform/chat-api/chatapi_test.go
git commit -m "feat(chat-api): add privileged_files option"
```

---

### Task 3: Path resolve helper

**Files:**
- Create or modify: `platform/chat-api/files.go` (or small `files_path.go`)
- Modify: `platform/chat-api/files_test.go`

**Step 1: Failing tests**

```go
func TestResolvePrivilegedPath(t *testing.T) {
	ws := t.TempDir()
	// "dir/a.txt" and "./dir/a.txt" → filepath.Join(ws, "dir/a.txt") abs
	// "~/x" → home/x (mock or use real UserHomeDir)
	// absolute stays absolute after Clean
	// "" → error
}
```

Reuse existing `expandHomeDir` from `workspace_bootstrap.go`. Relative paths: if not abs after home expand, `filepath.Join(workspace, path)` then `filepath.Abs`/`Clean`.

**Step 2–4:** Implement `resolvePrivilegedPath(workspace, path string) (string, error)`; run tests; commit.

```bash
git commit -m "feat(chat-api): resolve privileged file paths"
```

---

### Task 4: Privileged upload on `POST /files`

**Files:**
- Modify: `platform/chat-api/files.go` (`handleUploadFile`)
- Modify: `platform/chat-api/files_test.go`

**Step 1: Failing tests**

1. Privilege off + form `path=./out.txt` → `403`
2. Privilege on, upload to `subdir/out.txt`, no overwrite; file appears at `{workspace}/subdir/out.txt`; response has `path` (abs), no `id`; `GET /files` does not list it
3. Second upload same path without overwrite → `409`
4. `overwrite=true` → `200/201`, `overwritten: true`
5. Still enforce `max_upload_size`

**Step 2: Run — expect fail**

```bash
go test ./platform/chat-api/ -run TestPrivilegedUpload -count=1 -v
```

**Step 3: Implement in `handleUploadFile`**

After reading multipart:

```go
pathField := strings.TrimSpace(r.FormValue("path"))
if pathField != "" {
  if !p.privilegedFiles { writeErr 403; return }
  // resolve against workspaceDirForChannel
  // overwrite parse (true/1/yes)
  // if exists && !overwrite → 409
  // MkdirAll parent; WriteFile
  // writeOK with path/filename/mime/size/created_at/overwritten
  return
}
// else existing managed saveUploadedFile
```

**Step 4: Pass + commit**

```bash
git commit -m "feat(chat-api): privileged path upload on POST /files"
```

---

### Task 5: `GET /files/by-path` routing

**Files:**
- Modify: `platform/chat-api/files.go` (`handleFileRoutes`)
- Modify: `platform/chat-api/chatapi.go` if route order needs explicit handle (prefer branching inside `handleFileRoutes`)
- Modify: `platform/chat-api/files_test.go`

**Step 1: Failing tests**

1. Privilege off → `403`
2. Privilege on, write a file under workspace, `GET /v1/files/by-path?path=dir/f.txt` → `200` + bytes
3. Missing file → `404`
4. Existing `GET /files/{file_id}` still works for managed files

**Routing:** In `handleFileRoutes`, if `sub == "by-path"` (no further slash), handle by-path; else existing file_id logic. Ensure `files/by-path` is not rejected by `strings.Contains(sub, "/")` — currently any `/` in sub 404s; `by-path` has no slash so OK if query carries path.

**Step 2–4:** Implement; test; commit.

```bash
git commit -m "feat(chat-api): add GET /files/by-path privileged download"
```

---

### Task 6: Docs + config example

**Files:**
- Modify: `docs/chat-api.zh-CN.md` (§4.10 + options table + changelog row)
- Modify: `docs/chat-api.md` (brief mirror)
- Modify: `config.example.toml` (comment `privileged_files = false` + `max_upload_size` nearby)
- Modify: `docs/plans/2026-08-15-chat-api-privileged-files-design.md` status → implemented (when done)

**Step 1:** Document:

- Managed naming `file_<id>.<filename>`
- `privileged_files`
- Upload form `path` / `overwrite`
- `GET /files/by-path?path=`
- Security warning (host FS access)

**Step 2:** No code test; skim for consistency with design.

**Step 3: Commit**

```bash
git commit -m "docs: document chat-api privileged files and managed naming"
```

---

### Task 7: Full package verification

**Step 1:**

```bash
go test ./platform/chat-api/ -count=1
go build ./...
```

Expected: all green.

**Step 2:** Fix any regressions from naming change (`uploadedFilePath` must return new content path for `local_file` AppendFileRefs).

**Step 3:** Final commit only if fixes needed.

---

## Execution handoff

Plan saved to `docs/plans/2026-08-15-chat-api-privileged-files.md`.

**Two execution options:**

1. **Subagent-Driven (this session)** — fresh subagent per task, review between tasks  
2. **Parallel Session (separate)** — new session with executing-plans

Which approach?
