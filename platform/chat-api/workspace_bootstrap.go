package chatapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ensureChannelWorkspace prepares a per-channel workspace before the message
// reaches Engine multi-workspace resolution. It mirrors agent-runtime's
// workspace-bootstrap hook: create base_dir/<channel> and persist a binding
// so convention matching succeeds and plain chat (e.g. "hi") is not mistaken
// for a local directory path when workspace_init_allow_local_paths is enabled.
func (p *Platform) ensureChannelWorkspace(channelKey string) error {
	baseDir := strings.TrimSpace(p.multiWorkspaceBaseDir)
	if baseDir == "" {
		return nil
	}
	baseDir, err := expandHomeDir(baseDir)
	if err != nil {
		return err
	}

	channelName, err := p.ResolveChannelName(channelKey)
	if err != nil {
		return err
	}
	if channelName == "" {
		return nil
	}

	channelDir := filepath.Clean(filepath.Join(baseDir, channelName))
	cleanBase := filepath.Clean(baseDir)
	if channelDir != cleanBase && !strings.HasPrefix(channelDir, cleanBase+string(filepath.Separator)) {
		return fmt.Errorf("chat-api: channel workspace %q escapes base_dir %q", channelDir, cleanBase)
	}

	if err := os.MkdirAll(channelDir, 0o755); err != nil {
		return fmt.Errorf("chat-api: create channel workspace %q: %w", channelDir, err)
	}
	for _, rel := range []string{"files/chat/uploads", "files/chat/downloads"} {
		if err := os.MkdirAll(filepath.Join(channelDir, filepath.FromSlash(rel)), 0o755); err != nil {
			return fmt.Errorf("chat-api: create channel file directory %q: %w", rel, err)
		}
	}
	if err := p.ensureSharedKnowledgeWorkspace(cleanBase, channelDir); err != nil {
		return err
	}
	if p.projectName == "" || p.dataDir == "" {
		return nil
	}
	if err := p.bindChannelWorkspace(channelKey, channelName, channelDir); err != nil {
		return err
	}
	slog.Info("chat-api: channel workspace ready",
		"channel", channelName,
		"path", channelDir,
	)
	return nil
}

// ensureSharedKnowledgeWorkspace keeps knowledge and memory at project scope
// while exposing them at the same relative paths inside every channel workspace.
// Chat uploads and downloads remain channel-scoped.
func (p *Platform) ensureSharedKnowledgeWorkspace(baseDir, channelDir string) error {
	p.sharedWorkspaceMu.Lock()
	defer p.sharedWorkspaceMu.Unlock()

	sharedFilesDir := filepath.Join(baseDir, workspaceFilesDir)
	channelFilesDir := filepath.Join(channelDir, workspaceFilesDir)
	if err := os.MkdirAll(sharedFilesDir, 0o755); err != nil {
		return fmt.Errorf("chat-api: create shared files directory: %w", err)
	}
	if err := os.MkdirAll(channelFilesDir, 0o755); err != nil {
		return fmt.Errorf("chat-api: create channel files directory: %w", err)
	}
	for _, name := range []string{"knowledge", "memory"} {
		sharedDir := filepath.Join(sharedFilesDir, name)
		if err := os.MkdirAll(sharedDir, 0o755); err != nil {
			return fmt.Errorf("chat-api: create shared %s directory: %w", name, err)
		}
		channelPath := filepath.Join(channelFilesDir, name)
		if info, err := os.Lstat(channelPath); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				resolved, resolveErr := filepath.EvalSymlinks(channelPath)
				expected, expectedErr := filepath.EvalSymlinks(sharedDir)
				if resolveErr != nil || expectedErr != nil || resolved != expected {
					return fmt.Errorf("chat-api: channel %s link does not target project shared directory", name)
				}
				continue
			}
			if !info.IsDir() {
				return fmt.Errorf("chat-api: channel %s path already exists and is not a directory", name)
			}
			entries, readErr := os.ReadDir(channelPath)
			if readErr != nil {
				return fmt.Errorf("chat-api: inspect channel %s directory: %w", name, readErr)
			}
			if len(entries) != 0 {
				return fmt.Errorf("chat-api: channel %s directory is not empty; refusing to replace it", name)
			}
			if removeErr := os.Remove(channelPath); removeErr != nil {
				return fmt.Errorf("chat-api: remove empty channel %s directory: %w", name, removeErr)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("chat-api: inspect channel %s link: %w", name, err)
		}
		relTarget, err := filepath.Rel(channelFilesDir, sharedDir)
		if err != nil {
			return fmt.Errorf("chat-api: resolve shared %s link: %w", name, err)
		}
		if err := os.Symlink(relTarget, channelPath); err != nil && !os.IsExist(err) {
			return fmt.Errorf("chat-api: link shared %s directory: %w", name, err)
		}
	}
	return nil
}

func expandHomeDir(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("chat-api: resolve home dir: %w", err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

type persistedWorkspaceBinding struct {
	ChannelName string `json:"channel_name"`
	Workspace   string `json:"workspace"`
	BoundAt     string `json:"bound_at"`
}

func (p *Platform) bindChannelWorkspace(channelKey, channelName, workspace string) error {
	p.workspaceBindingMu.Lock()
	defer p.workspaceBindingMu.Unlock()

	storePath := filepath.Join(p.dataDir, "workspace_bindings.json")
	if err := os.MkdirAll(filepath.Dir(storePath), 0o755); err != nil {
		return fmt.Errorf("chat-api: create binding dir: %w", err)
	}

	bindings := map[string]map[string]persistedWorkspaceBinding{}
	if data, err := os.ReadFile(storePath); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &bindings); err != nil {
			// Bindings are derived from channel/workspace configuration. A prior
			// interrupted or concurrent write must not permanently break every
			// chat and file request; preserve the bad file for diagnosis and
			// rebuild it from the current request.
			corruptPath := fmt.Sprintf("%s.corrupt-%d", storePath, time.Now().UnixNano())
			if renameErr := os.Rename(storePath, corruptPath); renameErr != nil && !os.IsNotExist(renameErr) {
				return fmt.Errorf("chat-api: quarantine invalid workspace bindings: %w", renameErr)
			}
			slog.Error("chat-api: rebuilt invalid workspace bindings",
				"path", storePath,
				"corrupt_path", corruptPath,
				"error", err,
			)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("chat-api: read workspace bindings: %w", err)
	}

	projectKey := "project:" + p.projectName
	wsKey := p.Name() + ":" + channelKey
	if bindings[projectKey] == nil {
		bindings[projectKey] = map[string]persistedWorkspaceBinding{}
	}
	bindings[projectKey][wsKey] = persistedWorkspaceBinding{
		ChannelName: channelName,
		Workspace:   filepath.Clean(workspace),
		BoundAt:     time.Now().Format(time.RFC3339Nano),
	}

	data, err := json.MarshalIndent(bindings, "", "  ")
	if err != nil {
		return fmt.Errorf("chat-api: marshal workspace bindings: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(storePath), ".workspace-bindings-*.tmp")
	if err != nil {
		return fmt.Errorf("chat-api: create workspace bindings temp file: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chat-api: chmod workspace bindings temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chat-api: write workspace bindings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chat-api: sync workspace bindings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("chat-api: close workspace bindings: %w", err)
	}
	if err := os.Rename(tmpPath, storePath); err != nil {
		return fmt.Errorf("chat-api: replace workspace bindings: %w", err)
	}
	removeTemp = false
	return nil
}

func multiWorkspaceBaseDirFromOpts(opts map[string]any) string {
	for _, key := range []string{"base_dir", "cc_base_dir", "multi_workspace_base_dir"} {
		if v := strings.TrimSpace(stringOption(opts, key, "")); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(os.Getenv("AGENT_WORK_DIR")); v != "" {
		return v
	}
	return ""
}
