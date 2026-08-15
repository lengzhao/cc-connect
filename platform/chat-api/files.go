package chatapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	fileIDPrefix         = "file_"
	uploadMetaSuffix     = ".meta.json"
	workspaceUploadsDir  = "uploads"
	workspaceDownloadRel = ".cc-connect/chat-api/download"
	fileKindUpload       = "upload"
	fileKindDownload     = "download"
)

var fileIDPattern = regexp.MustCompile(`^file_[A-Za-z0-9_-]{22}$`)

type uploadedFileMeta struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Channel   string `json:"channel"`
	Filename  string `json:"filename"`
	MimeType  string `json:"mime_type"`
	Size      int64  `json:"size"`
	UserID    string `json:"user_id,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

type fileView struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Filename  string `json:"filename"`
	MimeType  string `json:"mime_type"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"created_at"`
	UserID    string `json:"user_id,omitempty"`
}

func toFileView(meta uploadedFileMeta) fileView {
	return fileView{
		ID:        meta.ID,
		Kind:      meta.Kind,
		Filename:  meta.Filename,
		MimeType:  meta.MimeType,
		Size:      meta.Size,
		CreatedAt: meta.CreatedAt,
		UserID:    meta.UserID,
	}
}

func (p *Platform) handleFiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p.handleListFiles(w, r)
	case http.MethodPost:
		p.handleUploadFile(w, r)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
	}
}

func (p *Platform) handleListFiles(w http.ResponseWriter, r *http.Request) {
	channelKey, ok := p.resolveChannel(w, r)
	if !ok {
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	switch kind {
	case "", "all":
		kind = "all"
	case fileKindUpload, fileKindDownload:
	default:
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))

	files, hasMore, nextCursor, err := p.listFiles(channelKey, kind, cursor, limit)
	if err != nil {
		if errors.Is(err, errWorkspaceNotConfigured) {
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if errors.Is(err, errUploadNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		slog.Error("chat-api: list files", "channel", channelKey, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	data := map[string]any{
		"limit":    clampLimit(limit),
		"has_more": hasMore,
		"files":    files,
	}
	if nextCursor != "" {
		data["next_cursor"] = nextCursor
	}
	writeOK(w, http.StatusOK, data)
}

func (p *Platform) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	user, ok := p.resolveUser(w, r, true)
	if !ok {
		return
	}
	channelKey, ok := p.resolveChannel(w, r)
	if !ok {
		return
	}
	if err := p.ensureChannelWorkspace(channelKey); err != nil {
		slog.Error("chat-api: ensure channel workspace", "channel", channelKey, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := r.ParseMultipartForm(p.maxUploadSize); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, p.maxUploadSize+1))
	if err != nil {
		slog.Error("chat-api: read upload", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if int64(len(data)) > p.maxUploadSize {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload too large")
		return
	}
	if len(data) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	filename := sanitizeUploadFilename(header.Filename)
	mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = http.DetectContentType(data)
	}
	if formMime := strings.TrimSpace(r.FormValue("mime_type")); formMime != "" {
		mimeType = formMime
	}

	pathField := strings.TrimSpace(r.FormValue("path"))
	if pathField != "" {
		p.handlePrivilegedUpload(w, channelKey, pathField, r.FormValue("overwrite"), filename, mimeType, data)
		return
	}

	meta, err := p.saveUploadedFile(channelKey, user, filename, mimeType, data)
	if err != nil {
		if errors.Is(err, errWorkspaceNotConfigured) {
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		slog.Error("chat-api: save upload", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeOK(w, http.StatusCreated, map[string]any{
		"id":         meta.ID,
		"filename":   meta.Filename,
		"mime_type":  meta.MimeType,
		"size":       meta.Size,
		"created_at": meta.CreatedAt,
	})
}

func parseOverwriteFlag(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

func (p *Platform) handlePrivilegedUpload(w http.ResponseWriter, channelKey, pathField, overwriteRaw, filename, mimeType string, data []byte) {
	if !p.privilegedFiles {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	workspace, err := p.workspaceDirForChannel(channelKey)
	if err != nil {
		if errors.Is(err, errWorkspaceNotConfigured) {
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		slog.Error("chat-api: privileged upload workspace", "channel", channelKey, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	target, err := resolvePrivilegedPath(workspace, pathField)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	wantOverwrite := parseOverwriteFlag(overwriteRaw)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		slog.Error("chat-api: privileged upload mkdir", "path", target, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	overwritten, err := writePrivilegedFile(target, data, wantOverwrite)
	if err != nil {
		if errors.Is(err, errPrivilegedFileExists) {
			writeErr(w, http.StatusConflict, "file exists")
			return
		}
		if errors.Is(err, errPrivilegedPathIsDir) {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		slog.Error("chat-api: privileged upload write", "path", target, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	outName := filename
	if outName == "" {
		outName = filepath.Base(target)
	}
	status := http.StatusCreated
	if overwritten {
		status = http.StatusOK
	}
	writeOK(w, status, map[string]any{
		"path":        target,
		"filename":    outName,
		"mime_type":   mimeType,
		"size":        int64(len(data)),
		"created_at":  time.Now().Unix(),
		"overwritten": overwritten,
	})
}

var (
	errPrivilegedFileExists = errors.New("privileged file exists")
	errPrivilegedPathIsDir  = errors.New("privileged path is directory")
)

// writePrivilegedFile writes data to target. When overwrite is false, uses
// O_CREATE|O_EXCL so concurrent creates cannot race past a Stat check.
func writePrivilegedFile(target string, data []byte, overwrite bool) (overwritten bool, err error) {
	if overwrite {
		if st, statErr := os.Stat(target); statErr == nil {
			if st.IsDir() {
				return false, errPrivilegedPathIsDir
			}
			overwritten = true
		} else if !os.IsNotExist(statErr) {
			return false, statErr
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return false, err
		}
		return overwritten, nil
	}

	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) || errors.Is(err, os.ErrExist) {
			return false, errPrivilegedFileExists
		}
		return false, err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(target)
		return false, writeErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return false, closeErr
	}
	return false, nil
}

func (p *Platform) handleFileRoutes(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, p.path+"files/")
	if sub == "" || strings.Contains(sub, "/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
		return
	}
	channelKey, ok := p.resolveChannel(w, r)
	if !ok {
		return
	}
	meta, data, err := p.loadFile(channelKey, sub)
	if err != nil {
		if errors.Is(err, errUploadNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		slog.Error("chat-api: load file", "file_id", sub, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if meta.MimeType != "" {
		w.Header().Set("Content-Type", meta.MimeType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	if meta.Filename != "" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": meta.Filename}))
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// SendFile saves an agent-produced file under workspace/.cc-connect/chat-api/download
// and emits a file_ready SSE event on the active run.
func (p *Platform) SendFile(_ context.Context, replyCtx any, file core.FileAttachment) error {
	if len(file.Data) == 0 {
		return fmt.Errorf("chat-api: empty file attachment")
	}
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil || rc.runID == "" {
		return fmt.Errorf("chat-api: unsupported send context %T", replyCtx)
	}
	run := p.pending.get(rc.runID)
	if run == nil {
		return fmt.Errorf("chat-api: run %q is not pending", rc.runID)
	}
	filename := sanitizeUploadFilename(file.FileName)
	if filename == "" {
		filename = "attachment"
	}
	mimeType := strings.TrimSpace(file.MimeType)
	if mimeType == "" {
		mimeType = http.DetectContentType(file.Data)
	}
	meta, err := p.saveDownloadFile(run.channelKey, filename, mimeType, file.Data)
	if err != nil {
		return fmt.Errorf("chat-api: save download file: %w", err)
	}
	run.enqueueEvent("file_ready", map[string]any{
		"message_id": rc.messageID,
		"file_id":    meta.ID,
		"filename":   meta.Filename,
		"mime_type":  meta.MimeType,
		"size":       meta.Size,
	})
	return nil
}

var (
	errUploadNotFound         = errors.New("upload not found")
	errWorkspaceNotConfigured = errors.New("workspace not configured")
)

func (p *Platform) workspaceDirForChannel(channelKey string) (string, error) {
	baseDir := strings.TrimSpace(p.multiWorkspaceBaseDir)
	if baseDir == "" {
		return "", errWorkspaceNotConfigured
	}
	baseDir, err := expandHomeDir(baseDir)
	if err != nil {
		return "", err
	}
	channelName, err := p.ResolveChannelName(channelKey)
	if err != nil {
		return "", err
	}
	if channelName == "" {
		return "", errWorkspaceNotConfigured
	}
	channelDir := filepath.Clean(filepath.Join(baseDir, channelName))
	cleanBase := filepath.Clean(baseDir)
	if channelDir != cleanBase && !strings.HasPrefix(channelDir, cleanBase+string(filepath.Separator)) {
		return "", fmt.Errorf("chat-api: channel workspace %q escapes base_dir", channelDir)
	}
	return channelDir, nil
}

func (p *Platform) channelUploadsDir(channelKey string) (string, error) {
	workspaceDir, err := p.workspaceDirForChannel(channelKey)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(workspaceDir, workspaceUploadsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir uploads: %w", err)
	}
	return dir, nil
}

func (p *Platform) channelDownloadDir(channelKey string) (string, error) {
	workspaceDir, err := p.workspaceDirForChannel(channelKey)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(workspaceDir, filepath.FromSlash(workspaceDownloadRel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir download: %w", err)
	}
	return dir, nil
}

func newFileID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("chat-api: generate file id: %w", err)
	}
	return fileIDPrefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (p *Platform) saveUploadedFile(channelKey, userID, filename, mimeType string, data []byte) (*uploadedFileMeta, error) {
	dir, err := p.channelUploadsDir(channelKey)
	if err != nil {
		return nil, err
	}
	return p.writeFileRecord(dir, channelKey, fileKindUpload, userID, filename, mimeType, data)
}

func (p *Platform) saveDownloadFile(channelKey, filename, mimeType string, data []byte) (*uploadedFileMeta, error) {
	dir, err := p.channelDownloadDir(channelKey)
	if err != nil {
		return nil, err
	}
	return p.writeFileRecord(dir, channelKey, fileKindDownload, "", filename, mimeType, data)
}

func (p *Platform) writeFileRecord(dir, channelKey, kind, userID, filename, mimeType string, data []byte) (*uploadedFileMeta, error) {
	id, err := newFileID()
	if err != nil {
		return nil, err
	}
	meta := &uploadedFileMeta{
		ID:        id,
		Kind:      kind,
		Channel:   channelKey,
		Filename:  filename,
		MimeType:  mimeType,
		Size:      int64(len(data)),
		UserID:    userID,
		CreatedAt: time.Now().Unix(),
	}
	contentPath := filepath.Join(dir, managedContentBaseName(id, filename))
	metaPath := contentPath + uploadMetaSuffix
	if err := os.WriteFile(contentPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("write content: %w", err)
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		_ = os.Remove(contentPath)
		return nil, err
	}
	if err := os.WriteFile(metaPath, raw, 0o644); err != nil {
		_ = os.Remove(contentPath)
		return nil, fmt.Errorf("write meta: %w", err)
	}
	return meta, nil
}

func managedContentBaseName(id, filename string) string {
	name := sanitizeUploadFilename(filename)
	if name == "" {
		name = "file"
	}
	return id + "." + name
}

// findManagedFilePaths looks up content+meta for fileID under dir.
// Prefer new layout file_<id>.<name>[.meta.json]; fall back to legacy file_<id>.
// If multiple new-layout metas exist for the same id, pick the lexicographically
// first meta path for deterministic behavior.
func findManagedFilePaths(dir, fileID string) (contentPath, metaPath string, err error) {
	if !fileIDPattern.MatchString(fileID) {
		return "", "", errUploadNotFound
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return "", "", errUploadNotFound
		}
		return "", "", readErr
	}
	prefix := fileID + "."
	var candidates []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, uploadMetaSuffix) {
			continue
		}
		// Skip legacy-shaped names that somehow match (fileID.meta.json has no extra segment).
		base := strings.TrimSuffix(name, uploadMetaSuffix)
		if base == fileID {
			continue
		}
		candidateMeta := filepath.Join(dir, name)
		candidateContent := strings.TrimSuffix(candidateMeta, uploadMetaSuffix)
		if _, statErr := os.Stat(candidateContent); statErr != nil {
			continue
		}
		candidates = append(candidates, candidateMeta)
	}
	if len(candidates) > 0 {
		sort.Strings(candidates)
		metaPath = candidates[0]
		contentPath = strings.TrimSuffix(metaPath, uploadMetaSuffix)
		return contentPath, metaPath, nil
	}

	legacyContent := filepath.Join(dir, fileID)
	legacyMeta := legacyContent + uploadMetaSuffix
	if _, statErr := os.Stat(legacyMeta); statErr != nil {
		if os.IsNotExist(statErr) {
			return "", "", errUploadNotFound
		}
		return "", "", statErr
	}
	if _, statErr := os.Stat(legacyContent); statErr != nil {
		if os.IsNotExist(statErr) {
			return "", "", errUploadNotFound
		}
		return "", "", statErr
	}
	return legacyContent, legacyMeta, nil
}

func (p *Platform) loadFile(channelKey, fileID string) (*uploadedFileMeta, []byte, error) {
	if !fileIDPattern.MatchString(fileID) {
		return nil, nil, errUploadNotFound
	}
	uploadDir, uploadErr := p.channelUploadsDir(channelKey)
	if uploadErr == nil {
		if meta, data, err := readFileRecord(uploadDir, fileID); err == nil {
			return meta, data, nil
		} else if !errors.Is(err, errUploadNotFound) {
			return nil, nil, err
		}
	} else if !errors.Is(uploadErr, errWorkspaceNotConfigured) {
		return nil, nil, uploadErr
	}
	downloadDir, downloadErr := p.channelDownloadDir(channelKey)
	if downloadErr != nil {
		if errors.Is(downloadErr, errWorkspaceNotConfigured) && errors.Is(uploadErr, errWorkspaceNotConfigured) {
			return nil, nil, errWorkspaceNotConfigured
		}
		return nil, nil, downloadErr
	}
	return readFileRecord(downloadDir, fileID)
}

func readFileRecord(dir, fileID string) (*uploadedFileMeta, []byte, error) {
	contentPath, metaPath, err := findManagedFilePaths(dir, fileID)
	if err != nil {
		return nil, nil, err
	}
	metaRaw, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, errUploadNotFound
		}
		return nil, nil, err
	}
	var meta uploadedFileMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(contentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, errUploadNotFound
		}
		return nil, nil, err
	}
	return &meta, data, nil
}

func (p *Platform) listFiles(channelKey, kind, cursor string, limit int) ([]fileView, bool, string, error) {
	var items []uploadedFileMeta
	uploadDir, uploadErr := p.channelUploadsDir(channelKey)
	downloadDir, downloadErr := p.channelDownloadDir(channelKey)
	if errors.Is(uploadErr, errWorkspaceNotConfigured) && errors.Is(downloadErr, errWorkspaceNotConfigured) {
		return nil, false, "", errWorkspaceNotConfigured
	}
	if kind == "all" || kind == fileKindUpload {
		if uploadErr != nil && !errors.Is(uploadErr, errWorkspaceNotConfigured) {
			return nil, false, "", uploadErr
		}
		if uploadErr == nil {
			metas, err := listFileMetasInDir(uploadDir, fileKindUpload)
			if err != nil {
				return nil, false, "", err
			}
			items = append(items, metas...)
		}
	}
	if kind == "all" || kind == fileKindDownload {
		if downloadErr != nil && !errors.Is(downloadErr, errWorkspaceNotConfigured) {
			return nil, false, "", downloadErr
		}
		if downloadErr == nil {
			metas, err := listFileMetasInDir(downloadDir, fileKindDownload)
			if err != nil {
				return nil, false, "", err
			}
			items = append(items, metas...)
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt != items[j].CreatedAt {
			return items[i].CreatedAt > items[j].CreatedAt
		}
		return items[i].ID > items[j].ID
	})

	limit = clampLimit(limit)
	start := 0
	if cursor != "" {
		if !fileIDPattern.MatchString(cursor) {
			return nil, false, "", errUploadNotFound
		}
		found := false
		for i, item := range items {
			if item.ID == cursor {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, false, "", errUploadNotFound
		}
	}

	end := start + limit
	hasMore := end < len(items)
	if end > len(items) {
		end = len(items)
	}
	page := items[start:end]
	out := make([]fileView, len(page))
	for i, meta := range page {
		out[i] = toFileView(meta)
	}
	var nextCursor string
	if hasMore && len(page) > 0 {
		nextCursor = page[len(page)-1].ID
	}
	return out, hasMore, nextCursor, nil
}

func listFileMetasInDir(dir, kind string) ([]uploadedFileMeta, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []uploadedFileMeta
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), uploadMetaSuffix) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var meta uploadedFileMeta
		if err := json.Unmarshal(raw, &meta); err != nil {
			continue
		}
		if meta.ID == "" {
			base := strings.TrimSuffix(entry.Name(), uploadMetaSuffix)
			meta.ID = managedFileIDFromDiskName(base)
		}
		if meta.ID == "" {
			continue
		}
		if meta.Kind == "" {
			meta.Kind = kind
		}
		out = append(out, meta)
	}
	return out, nil
}

// managedFileIDFromDiskName extracts file_<id> from a managed content basename
// (legacy file_<id> or new file_<id>.<filename>).
func managedFileIDFromDiskName(base string) string {
	if fileIDPattern.MatchString(base) {
		return base
	}
	dot := strings.Index(base, ".")
	if dot < 0 {
		return ""
	}
	candidate := base[:dot]
	if fileIDPattern.MatchString(candidate) {
		return candidate
	}
	return ""
}

func (p *Platform) uploadedFilePath(channelKey, fileID string) (string, *uploadedFileMeta, error) {
	dir, err := p.channelUploadsDir(channelKey)
	if err != nil {
		return "", nil, err
	}
	contentPath, metaPath, err := findManagedFilePaths(dir, fileID)
	if err != nil {
		return "", nil, err
	}
	metaRaw, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, errUploadNotFound
		}
		return "", nil, err
	}
	var meta uploadedFileMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return "", nil, err
	}
	return contentPath, &meta, nil
}

func sanitizeUploadFilename(name string) string {
	name = filepath.ToSlash(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}

func (p *Platform) resolveUploadedInput(channelKey string, in chatInput) (path string, data []byte, mimeType, filename string, err error) {
	fileID := strings.TrimSpace(in.UploadFileID)
	if fileID == "" {
		return "", nil, "", "", fmt.Errorf("upload_file_id required")
	}
	filePath, meta, err := p.uploadedFilePath(channelKey, fileID)
	if err != nil {
		return "", nil, "", "", err
	}
	filename = meta.Filename
	if strings.TrimSpace(in.Filename) != "" {
		filename = sanitizeUploadFilename(in.Filename)
	}
	mimeType = meta.MimeType
	if strings.TrimSpace(in.MimeType) != "" {
		mimeType = in.MimeType
	}
	return filePath, nil, mimeType, filename, nil
}

func (p *Platform) inputsToCore(channelKey string, inputs []chatInput) ([]core.ImageAttachment, []core.FileAttachment, *core.AudioAttachment, []string, error) {
	var images []core.ImageAttachment
	var files []core.FileAttachment
	var filePaths []string
	var audio *core.AudioAttachment
	for _, in := range inputs {
		method := strings.ToLower(strings.TrimSpace(in.TransferMethod))
		if method == "" {
			method = "base64"
		}
		var data []byte
		var diskPath string
		var err error
		mimeType := in.MimeType
		filename := in.Filename
		switch method {
		case "base64":
			data, err = base64.StdEncoding.DecodeString(in.Data)
			if err != nil {
				return nil, nil, nil, nil, err
			}
		case "local_file":
			diskPath, data, mimeType, filename, err = p.resolveUploadedInput(channelKey, in)
			if err != nil {
				if errors.Is(err, errUploadNotFound) {
					return nil, nil, nil, nil, fmt.Errorf("upload not found")
				}
				return nil, nil, nil, nil, err
			}
		default:
			return nil, nil, nil, nil, fmt.Errorf("unsupported transfer_method")
		}
		switch strings.ToLower(in.Type) {
		case "image":
			if len(data) == 0 && diskPath != "" {
				data, err = os.ReadFile(diskPath)
				if err != nil {
					return nil, nil, nil, nil, err
				}
			}
			images = append(images, core.ImageAttachment{
				MimeType: mimeType,
				Data:     data,
				FileName: filename,
			})
		case "file":
			if diskPath != "" {
				filePaths = append(filePaths, diskPath)
				continue
			}
			files = append(files, core.FileAttachment{
				MimeType: mimeType,
				Data:     data,
				FileName: filename,
			})
		case "audio":
			if audio != nil {
				return nil, nil, nil, nil, fmt.Errorf("only one audio input supported")
			}
			if len(data) == 0 && diskPath != "" {
				data, err = os.ReadFile(diskPath)
				if err != nil {
					return nil, nil, nil, nil, err
				}
			}
			audio = &core.AudioAttachment{
				MimeType: mimeType,
				Data:     data,
			}
		default:
			return nil, nil, nil, nil, fmt.Errorf("unsupported input type")
		}
	}
	return images, files, audio, filePaths, nil
}
