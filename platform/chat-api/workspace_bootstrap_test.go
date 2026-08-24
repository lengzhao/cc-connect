package chatapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestMultiWorkspaceBaseDirFromOpts(t *testing.T) {
	t.Setenv("AGENT_WORK_DIR", "")
	if got := multiWorkspaceBaseDirFromOpts(map[string]any{"base_dir": "/data/workspaces"}); got != "/data/workspaces" {
		t.Fatalf("base_dir = %q", got)
	}
	if got := multiWorkspaceBaseDirFromOpts(map[string]any{"cc_base_dir": "/cc/base"}); got != "/cc/base" {
		t.Fatalf("cc_base_dir = %q", got)
	}
	t.Setenv("AGENT_WORK_DIR", "/from/env")
	if got := multiWorkspaceBaseDirFromOpts(nil); got != "/from/env" {
		t.Fatalf("AGENT_WORK_DIR = %q", got)
	}
}

func TestEnsureChannelWorkspaceCreatesDirAndBinding(t *testing.T) {
	dataDir := t.TempDir()
	baseDir := t.TempDir()
	p := &Platform{
		projectName:           "demo",
		dataDir:               dataDir,
		multiWorkspaceBaseDir: baseDir,
	}

	if err := p.ensureChannelWorkspace("chat-123"); err != nil {
		t.Fatalf("ensureChannelWorkspace: %v", err)
	}

	channelDir := filepath.Join(baseDir, "chat-123")
	if st, err := os.Stat(channelDir); err != nil || !st.IsDir() {
		t.Fatalf("channel dir missing: %v", err)
	}
	for _, rel := range []string{"files/chat/uploads", "files/chat/downloads"} {
		if st, err := os.Stat(filepath.Join(channelDir, filepath.FromSlash(rel))); err != nil || !st.IsDir() {
			t.Fatalf("channel directory %s missing: %v", rel, err)
		}
	}
	for _, rel := range []string{"files/memory", "files/knowledge"} {
		if st, err := os.Stat(filepath.Join(baseDir, filepath.FromSlash(rel))); err != nil || !st.IsDir() {
			t.Fatalf("project shared directory %s missing: %v", rel, err)
		}
	}

	storePath := filepath.Join(dataDir, "workspace_bindings.json")
	raw, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read bindings: %v", err)
	}
	var bindings map[string]map[string]persistedWorkspaceBinding
	if err := json.Unmarshal(raw, &bindings); err != nil {
		t.Fatalf("decode bindings: %v", err)
	}
	b := bindings["project:demo"]["chat-api:chat-123"]
	if b.ChannelName != "chat-123" || b.Workspace != channelDir {
		t.Fatalf("binding = %+v, want channel chat-123 workspace %q", b, channelDir)
	}
}

func TestEnsureChannelWorkspaceCreatesDefaultChannel(t *testing.T) {
	dataDir := t.TempDir()
	baseDir := t.TempDir()
	p := &Platform{
		projectName:           "demo",
		dataDir:               dataDir,
		multiWorkspaceBaseDir: baseDir,
	}
	if err := p.ensureChannelWorkspace(defaultWorkspaceChannelID); err != nil {
		t.Fatalf("ensureChannelWorkspace default: %v", err)
	}
	channelDir := filepath.Join(baseDir, defaultWorkspaceChannelID)
	if st, err := os.Stat(channelDir); err != nil || !st.IsDir() {
		t.Fatalf("default channel dir missing: %v", err)
	}
	storePath := filepath.Join(dataDir, "workspace_bindings.json")
	raw, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read bindings: %v", err)
	}
	var bindings map[string]map[string]persistedWorkspaceBinding
	if err := json.Unmarshal(raw, &bindings); err != nil {
		t.Fatalf("decode bindings: %v", err)
	}
	b := bindings["project:demo"]["chat-api:"+defaultWorkspaceChannelID]
	if b.ChannelName != defaultWorkspaceChannelID || b.Workspace != channelDir {
		t.Fatalf("binding = %+v, want channel %s workspace %q", b, defaultWorkspaceChannelID, channelDir)
	}
}

func TestEnsureChannelWorkspaceConcurrentBindings(t *testing.T) {
	dataDir := t.TempDir()
	baseDir := t.TempDir()
	p := &Platform{projectName: "demo", dataDir: dataDir, multiWorkspaceBaseDir: baseDir}

	const count = 32
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- p.ensureChannelWorkspace("chat-123")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ensureChannelWorkspace: %v", err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dataDir, "workspace_bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var bindings map[string]map[string]persistedWorkspaceBinding
	if err := json.Unmarshal(raw, &bindings); err != nil {
		t.Fatalf("bindings corrupted: %v: %q", err, raw)
	}
}

func TestEnsureChannelWorkspaceRepairsInvalidBindings(t *testing.T) {
	dataDir := t.TempDir()
	baseDir := t.TempDir()
	storePath := filepath.Join(dataDir, "workspace_bindings.json")
	if err := os.WriteFile(storePath, []byte(`{"project:demo":`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &Platform{projectName: "demo", dataDir: dataDir, multiWorkspaceBaseDir: baseDir}
	if err := p.ensureChannelWorkspace("chat-123"); err != nil {
		t.Fatalf("repair invalid bindings: %v", err)
	}
	raw, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var bindings map[string]map[string]persistedWorkspaceBinding
	if err := json.Unmarshal(raw, &bindings); err != nil {
		t.Fatalf("repaired bindings invalid: %v: %q", err, raw)
	}
	corrupt, err := filepath.Glob(storePath + ".corrupt-*")
	if err != nil || len(corrupt) != 1 {
		t.Fatalf("corrupt backup = %v, err=%v", corrupt, err)
	}
}
