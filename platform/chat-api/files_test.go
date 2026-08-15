package chatapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func testWorkspacePlatform(t *testing.T) (*Platform, string) {
	t.Helper()
	baseDir := t.TempDir()
	p := newTestPlatform(t, map[string]any{
		"api_token":   "secret",
		"cc_data_dir": t.TempDir(),
		"cc_project":  "testproj",
		"base_dir":    baseDir,
	})
	return p, baseDir
}

func setFileHeaders(req *http.Request, token, user string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Chat-API-User", user)
	req.Header.Set("X-Chat-API-Channel", testChannel)
}

func TestListFiles(t *testing.T) {
	p, _ := testWorkspacePlatform(t)
	uploadMeta, err := p.saveUploadedFile(testChannel, "user_001", "in.txt", "text/plain", []byte("in"))
	if err != nil {
		t.Fatal(err)
	}
	downloadMeta, err := p.saveDownloadFile(testChannel, "out.txt", "text/plain", []byte("out"))
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	setChatReadHeaders(req, "secret")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			Files []fileView `json:"files"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data.Files) != 2 {
		t.Fatalf("files len = %d, want 2", len(resp.Data.Files))
	}
	ids := map[string]bool{resp.Data.Files[0].ID: true, resp.Data.Files[1].ID: true}
	if !ids[uploadMeta.ID] || !ids[downloadMeta.ID] {
		t.Fatalf("files = %#v", resp.Data.Files)
	}

	reqKind := httptest.NewRequest(http.MethodGet, "/v1/files?kind=download", nil)
	setChatReadHeaders(reqKind, "secret")
	recKind := httptest.NewRecorder()
	p.routes().ServeHTTP(recKind, reqKind)
	if recKind.Code != http.StatusOK {
		t.Fatalf("kind filter status = %d", recKind.Code)
	}
	if err := json.Unmarshal(recKind.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data.Files) != 1 || resp.Data.Files[0].ID != downloadMeta.ID || resp.Data.Files[0].Kind != fileKindDownload {
		t.Fatalf("download-only = %#v", resp.Data.Files)
	}
}

func TestUploadAndDownloadFile(t *testing.T) {
	p, baseDir := testWorkspacePlatform(t)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("hello upload")); err != nil {
		t.Fatal(err)
	}
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/files", body)
	setFileHeaders(req, "secret", "user_001")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var uploadResp struct {
		OK   bool `json:"ok"`
		Data struct {
			ID       string `json:"id"`
			Filename string `json:"filename"`
			MimeType string `json:"mime_type"`
			Size     int64  `json:"size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &uploadResp); err != nil {
		t.Fatal(err)
	}
	if !uploadResp.OK || !strings.HasPrefix(uploadResp.Data.ID, fileIDPrefix) {
		t.Fatalf("upload resp = %#v", uploadResp)
	}
	if uploadResp.Data.Filename != "notes.txt" || uploadResp.Data.Size != 12 {
		t.Fatalf("upload meta = %#v", uploadResp.Data)
	}

	uploadPath := filepath.Join(baseDir, testChannel, workspaceUploadsDir, managedContentBaseName(uploadResp.Data.ID, uploadResp.Data.Filename))
	if _, err := os.Stat(uploadPath); err != nil {
		t.Fatalf("upload not on disk at %q: %v", uploadPath, err)
	}

	dlReq := httptest.NewRequest(http.MethodGet, "/v1/files/"+uploadResp.Data.ID, nil)
	setChatReadHeaders(dlReq, "secret")
	dlRec := httptest.NewRecorder()
	p.routes().ServeHTTP(dlRec, dlReq)
	if dlRec.Code != http.StatusOK {
		t.Fatalf("download status = %d, body = %s", dlRec.Code, dlRec.Body.String())
	}
	if got := dlRec.Body.String(); got != "hello upload" {
		t.Fatalf("download body = %q", got)
	}
}

func TestUploadFileRequiresUserAndChannel(t *testing.T) {
	p, _ := testWorkspacePlatform(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing user)", rec.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(""))
	req2.Header.Set("Authorization", "Bearer secret")
	req2.Header.Set("X-Chat-API-User", "user_001")
	rec2 := httptest.NewRecorder()
	p.routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing channel)", rec2.Code)
	}
}

func TestDownloadFileNotFound(t *testing.T) {
	p, _ := testWorkspacePlatform(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/files/file_abcdefghijklmnopqrstuv", nil)
	setChatReadHeaders(req, "secret")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestInputsToCoreLocalFileUsesWorkspacePath(t *testing.T) {
	p, baseDir := testWorkspacePlatform(t)
	meta, err := p.saveUploadedFile(testChannel, "user_001", "doc.pdf", "application/pdf", []byte("%PDF-1.4"))
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(baseDir, testChannel, workspaceUploadsDir, managedContentBaseName(meta.ID, "doc.pdf"))

	images, files, audio, paths, err := p.inputsToCore(testChannel, []chatInput{{
		Type:           "file",
		TransferMethod: "local_file",
		UploadFileID:   meta.ID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 0 || audio != nil || len(files) != 0 {
		t.Fatalf("attachments = images=%d files=%d audio=%v", len(images), len(files), audio)
	}
	if len(paths) != 1 || paths[0] != wantPath {
		t.Fatalf("paths = %#v, want %q", paths, wantPath)
	}
}

func TestUploadWithoutWorkspace(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"api_token": "secret"})

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "x.txt")
	_, _ = part.Write([]byte("x"))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/files", body)
	setFileHeaders(req, "secret", "user_001")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestUploadTooLarge(t *testing.T) {
	p, _ := testWorkspacePlatform(t)
	p.maxUploadSize = 8

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "big.bin")
	_, _ = io.Copy(part, strings.NewReader("0123456789"))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/files", body)
	setFileHeaders(req, "secret", "user_001")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestSendFileStoresInDownloadDirAndEmitsSSE(t *testing.T) {
	p, baseDir := testWorkspacePlatform(t)
	sse, err := newSSEWriter(httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	run := newRunState("run_file", "user_001", testChannel, "sk", "conv", "conv:0", p, sse, time.Now().Add(time.Minute))
	if !p.pending.create(run) {
		t.Fatal("create run")
	}
	rc := &replyContext{runID: run.id, messageID: run.messageID}

	if err := p.SendFile(context.Background(), rc, core.FileAttachment{
		MimeType: "text/plain",
		Data:     []byte("agent output"),
		FileName: "out.txt",
	}); err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	downloadPath := filepath.Join(baseDir, testChannel, filepath.FromSlash(workspaceDownloadRel))
	entries, err := os.ReadDir(downloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("download dir entries = %d, want at least 2 (content+meta)", len(entries))
	}

	run.mu.Lock()
	events := append([]pendingSSEEvent(nil), run.pendingEvents...)
	run.mu.Unlock()
	if len(events) != 1 || events[0].name != "file_ready" {
		t.Fatalf("events = %#v", events)
	}
	payload, ok := events[0].payload.(map[string]any)
	if !ok || payload["filename"] != "out.txt" {
		t.Fatalf("payload = %#v", events[0].payload)
	}
}

func TestSanitizeUploadFilename(t *testing.T) {
	if got := sanitizeUploadFilename("../../etc/passwd"); got != "passwd" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeUploadFilename(""); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestManagedContentBaseName(t *testing.T) {
	id := "file_abcdefghijklmnopqrstuv"
	if got := managedContentBaseName(id, "report.pdf"); got != id+".report.pdf" {
		t.Fatalf("got %q, want %q", got, id+".report.pdf")
	}
	if got := managedContentBaseName(id, ""); got != id+".file" {
		t.Fatalf("empty name got %q, want %q", got, id+".file")
	}
	if got := managedContentBaseName(id, "../../etc/passwd"); got != id+".passwd" {
		t.Fatalf("path traversal got %q, want %q", got, id+".passwd")
	}
	if got := managedContentBaseName(id, "."); got != id+".file" {
		t.Fatalf("dot name got %q, want %q", got, id+".file")
	}
}

func TestFindManagedFilePaths_NewAndLegacy(t *testing.T) {
	dir := t.TempDir()
	id := "file_abcdefghijklmnopqrstuv"

	legacyContent := filepath.Join(dir, id)
	legacyMeta := legacyContent + uploadMetaSuffix
	if err := os.WriteFile(legacyContent, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyMeta, []byte(`{"id":"`+id+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	contentPath, metaPath, err := findManagedFilePaths(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if contentPath != legacyContent || metaPath != legacyMeta {
		t.Fatalf("legacy: content=%q meta=%q", contentPath, metaPath)
	}

	newContent := filepath.Join(dir, id+".notes.txt")
	newMeta := newContent + uploadMetaSuffix
	if err := os.WriteFile(newContent, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newMeta, []byte(`{"id":"`+id+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	contentPath, metaPath, err = findManagedFilePaths(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if contentPath != newContent || metaPath != newMeta {
		t.Fatalf("new: content=%q meta=%q, want content=%q meta=%q", contentPath, metaPath, newContent, newMeta)
	}

	missingDir := t.TempDir()
	if _, _, err := findManagedFilePaths(missingDir, id); !errors.Is(err, errUploadNotFound) {
		t.Fatalf("missing: err = %v, want errUploadNotFound", err)
	}
}

func TestFindManagedFilePaths_MultiMatchDeterministic(t *testing.T) {
	dir := t.TempDir()
	id := "file_abcdefghijklmnopqrstuv"

	// Write two new-layout pairs; lexicographically first meta path should win.
	laterContent := filepath.Join(dir, id+".zzz.txt")
	laterMeta := laterContent + uploadMetaSuffix
	earlierContent := filepath.Join(dir, id+".aaa.txt")
	earlierMeta := earlierContent + uploadMetaSuffix
	for _, pair := range [][2]string{
		{laterContent, laterMeta},
		{earlierContent, earlierMeta},
	} {
		if err := os.WriteFile(pair[0], []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pair[1], []byte(`{"id":"`+id+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	contentPath, metaPath, err := findManagedFilePaths(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if contentPath != earlierContent || metaPath != earlierMeta {
		t.Fatalf("got content=%q meta=%q, want content=%q meta=%q", contentPath, metaPath, earlierContent, earlierMeta)
	}
}

func TestManagedFileIDFromDiskName(t *testing.T) {
	id := "file_abcdefghijklmnopqrstuv"
	if got := managedFileIDFromDiskName(id); got != id {
		t.Fatalf("legacy basename: got %q, want %q", got, id)
	}
	if got := managedFileIDFromDiskName(id + ".a.b"); got != id {
		t.Fatalf("dotted filename: got %q, want %q", got, id)
	}
	if got := managedFileIDFromDiskName("not-a-file"); got != "" {
		t.Fatalf("illegal name: got %q, want empty", got)
	}
	if got := managedFileIDFromDiskName("file_short.name"); got != "" {
		t.Fatalf("short id: got %q, want empty", got)
	}
}

func TestListFileMetasInDir_EmptyIDDerivation(t *testing.T) {
	dir := t.TempDir()
	id := "file_abcdefghijklmnopqrstuv"

	validMeta := filepath.Join(dir, id+".notes.txt"+uploadMetaSuffix)
	if err := os.WriteFile(validMeta, []byte(`{"filename":"notes.txt","kind":"upload"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Unparseable opaque id from disk name — must be skipped, not listed with full basename.
	if err := os.WriteFile(filepath.Join(dir, "garbage.meta.json"), []byte(`{"filename":"garbage"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	metas, err := listFileMetasInDir(dir, fileKindUpload)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("metas len = %d, want 1 (skip empty id): %#v", len(metas), metas)
	}
	if metas[0].ID != id {
		t.Fatalf("derived id = %q, want opaque %q (not full file_<id>.name)", metas[0].ID, id)
	}
}

func TestResolvePrivilegedPath(t *testing.T) {
	ws := t.TempDir()

	wantRel, err := filepath.Abs(filepath.Join(ws, "dir", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"dir/a.txt", "./dir/a.txt"} {
		got, err := resolvePrivilegedPath(ws, in)
		if err != nil {
			t.Fatalf("resolvePrivilegedPath(%q): %v", in, err)
		}
		if got != wantRel {
			t.Fatalf("resolvePrivilegedPath(%q) = %q, want %q", in, got, wantRel)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	wantHome, err := filepath.Abs(filepath.Join(home, "x"))
	if err != nil {
		t.Fatal(err)
	}
	gotHome, err := resolvePrivilegedPath(ws, "~/x")
	if err != nil {
		t.Fatalf("~/x: %v", err)
	}
	if gotHome != wantHome {
		t.Fatalf("~/x = %q, want %q", gotHome, wantHome)
	}

	absIn := filepath.Join(ws, "abs.txt")
	wantAbs, err := filepath.Abs(filepath.Clean(absIn))
	if err != nil {
		t.Fatal(err)
	}
	gotAbs, err := resolvePrivilegedPath(ws, absIn)
	if err != nil {
		t.Fatalf("absolute: %v", err)
	}
	if gotAbs != wantAbs {
		t.Fatalf("absolute = %q, want %q", gotAbs, wantAbs)
	}

	parentWant, err := filepath.Abs(filepath.Join(ws, "..", "outside.txt"))
	if err != nil {
		t.Fatal(err)
	}
	gotParent, err := resolvePrivilegedPath(ws, "../outside.txt")
	if err != nil {
		t.Fatalf("../outside.txt: %v", err)
	}
	if gotParent != parentWant {
		t.Fatalf("../outside.txt = %q, want %q", gotParent, parentWant)
	}

	for _, empty := range []string{"", "   ", "\t"} {
		if _, err := resolvePrivilegedPath(ws, empty); err == nil {
			t.Fatalf("empty path %q: want error", empty)
		}
	}
}

func privilegedUploadBody(t *testing.T, filename, content, path, overwrite string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if path != "" {
		if err := writer.WriteField("path", path); err != nil {
			t.Fatal(err)
		}
	}
	if overwrite != "" {
		if err := writer.WriteField("overwrite", overwrite); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func TestPrivilegedUpload_ForbiddenWhenDisabled(t *testing.T) {
	p, _ := testWorkspacePlatform(t)

	body, ctype := privilegedUploadBody(t, "out.txt", "x", "./out.txt", "")
	req := httptest.NewRequest(http.MethodPost, "/v1/files", body)
	setFileHeaders(req, "secret", "user_001")
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}

func TestPrivilegedUpload_WritesWorkspacePath(t *testing.T) {
	p, baseDir := testWorkspacePlatform(t)
	p.privilegedFiles = true

	body, ctype := privilegedUploadBody(t, "out.txt", "hello path", "subdir/out.txt", "")
	req := httptest.NewRequest(http.MethodPost, "/v1/files", body)
	setFileHeaders(req, "secret", "user_001")
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200/201, body = %s", rec.Code, rec.Body.String())
	}

	var uploadResp struct {
		OK   bool `json:"ok"`
		Data struct {
			ID          string `json:"id"`
			Path        string `json:"path"`
			Filename    string `json:"filename"`
			MimeType    string `json:"mime_type"`
			Size        int64  `json:"size"`
			CreatedAt   int64  `json:"created_at"`
			Overwritten bool   `json:"overwritten"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &uploadResp); err != nil {
		t.Fatal(err)
	}
	if !uploadResp.OK {
		t.Fatalf("upload resp = %#v", uploadResp)
	}
	if uploadResp.Data.ID != "" {
		t.Fatalf("path upload must not return id, got %q", uploadResp.Data.ID)
	}
	wantPath, err := filepath.Abs(filepath.Join(baseDir, testChannel, "subdir", "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if uploadResp.Data.Path != wantPath {
		t.Fatalf("path = %q, want %q", uploadResp.Data.Path, wantPath)
	}
	if uploadResp.Data.Filename != "out.txt" || uploadResp.Data.Size != 10 || uploadResp.Data.Overwritten {
		t.Fatalf("meta = %#v", uploadResp.Data)
	}
	if uploadResp.Data.CreatedAt == 0 {
		t.Fatal("created_at missing")
	}

	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("file not on disk: %v", err)
	}
	if string(got) != "hello path" {
		t.Fatalf("disk content = %q", got)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	setChatReadHeaders(listReq, "secret")
	listRec := httptest.NewRecorder()
	p.routes().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d", listRec.Code)
	}
	var listResp struct {
		Data struct {
			Files []fileView `json:"files"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Data.Files) != 0 {
		t.Fatalf("privileged upload must not appear in list: %#v", listResp.Data.Files)
	}
}

func TestPrivilegedUpload_ConflictWithoutOverwrite(t *testing.T) {
	p, _ := testWorkspacePlatform(t)
	p.privilegedFiles = true

	body1, ctype1 := privilegedUploadBody(t, "out.txt", "first", "subdir/out.txt", "")
	req1 := httptest.NewRequest(http.MethodPost, "/v1/files", body1)
	setFileHeaders(req1, "secret", "user_001")
	req1.Header.Set("Content-Type", ctype1)
	rec1 := httptest.NewRecorder()
	p.routes().ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated && rec1.Code != http.StatusOK {
		t.Fatalf("first upload status = %d, body = %s", rec1.Code, rec1.Body.String())
	}

	body2, ctype2 := privilegedUploadBody(t, "out.txt", "second", "subdir/out.txt", "")
	req2 := httptest.NewRequest(http.MethodPost, "/v1/files", body2)
	setFileHeaders(req2, "secret", "user_001")
	req2.Header.Set("Content-Type", ctype2)
	rec2 := httptest.NewRecorder()
	p.routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second upload status = %d, want 409, body = %s", rec2.Code, rec2.Body.String())
	}
}

func TestPrivilegedUpload_Overwrite(t *testing.T) {
	p, baseDir := testWorkspacePlatform(t)
	p.privilegedFiles = true

	body1, ctype1 := privilegedUploadBody(t, "out.txt", "first", "subdir/out.txt", "")
	req1 := httptest.NewRequest(http.MethodPost, "/v1/files", body1)
	setFileHeaders(req1, "secret", "user_001")
	req1.Header.Set("Content-Type", ctype1)
	rec1 := httptest.NewRecorder()
	p.routes().ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated && rec1.Code != http.StatusOK {
		t.Fatalf("first upload status = %d, body = %s", rec1.Code, rec1.Body.String())
	}

	body2, ctype2 := privilegedUploadBody(t, "out.txt", "replaced", "subdir/out.txt", "true")
	req2 := httptest.NewRequest(http.MethodPost, "/v1/files", body2)
	setFileHeaders(req2, "secret", "user_001")
	req2.Header.Set("Content-Type", ctype2)
	rec2 := httptest.NewRecorder()
	p.routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated && rec2.Code != http.StatusOK {
		t.Fatalf("overwrite status = %d, want 200/201, body = %s", rec2.Code, rec2.Body.String())
	}
	var uploadResp struct {
		OK   bool `json:"ok"`
		Data struct {
			Overwritten bool   `json:"overwritten"`
			Size        int64  `json:"size"`
			Path        string `json:"path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &uploadResp); err != nil {
		t.Fatal(err)
	}
	if !uploadResp.OK || !uploadResp.Data.Overwritten || uploadResp.Data.Size != 8 {
		t.Fatalf("overwrite resp = %#v", uploadResp)
	}
	wantPath := filepath.Join(baseDir, testChannel, "subdir", "out.txt")
	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "replaced" {
		t.Fatalf("disk content = %q", got)
	}
}

func TestPrivilegedUpload_EnforcesMaxSize(t *testing.T) {
	p, _ := testWorkspacePlatform(t)
	p.privilegedFiles = true
	p.maxUploadSize = 8

	body, ctype := privilegedUploadBody(t, "big.bin", "0123456789", "big.bin", "")
	req := httptest.NewRequest(http.MethodPost, "/v1/files", body)
	setFileHeaders(req, "secret", "user_001")
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413, body = %s", rec.Code, rec.Body.String())
	}
}
