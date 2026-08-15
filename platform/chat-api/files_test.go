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
