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
	for _, rel := range []string{"files/chat/uploads", "files/chat/downloads", "files/memory", "files/knowledge"} {
		if st, err := os.Stat(filepath.Join(channelDir, filepath.FromSlash(rel))); err != nil || !st.IsDir() {
			t.Fatalf("shared directory %s missing: %v", rel, err)
		}
	}
	for _, name := range []string{"knowledge", "memory"} {
		link := filepath.Join(channelDir, "files", name)
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is not a shared symlink: info=%v err=%v", link, info, err)
		}
		expected, err := filepath.EvalSymlinks(filepath.Join(baseDir, "files", name))
		if err != nil {
			t.Fatal(err)
		}
		if resolved, err := filepath.EvalSymlinks(link); err != nil || resolved != expected {
			t.Fatalf("%s resolves to %q, err=%v", link, resolved, err)
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

func TestEnsureChannelWorkspaceSharesKnowledgeAcrossChannels(t *testing.T) {
	dataDir := t.TempDir()
	baseDir := t.TempDir()
	p := &Platform{projectName: "demo", dataDir: dataDir, multiWorkspaceBaseDir: baseDir}
	for _, channel := range []string{"nex-training", "lts-tool:task-1"} {
		if err := p.ensureChannelWorkspace(channel); err != nil {
			t.Fatalf("ensure %s: %v", channel, err)
		}
	}
	first := filepath.Join(baseDir, "nex-training", "files", "knowledge", "guide.md")
	if err := os.WriteFile(first, []byte("shared"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(baseDir, "lts-tool:task-1", "files", "knowledge", "guide.md")
	if got, err := os.ReadFile(second); err != nil || string(got) != "shared" {
		t.Fatalf("second channel read = %q, err=%v", got, err)
	}
}

func TestEnsureChannelWorkspaceReplacesEmptyLegacyDirectories(t *testing.T) {
	dataDir := t.TempDir()
	baseDir := t.TempDir()
	channelFiles := filepath.Join(baseDir, "nex-training", "files")
	for _, name := range []string{"knowledge", "memory"} {
		if err := os.MkdirAll(filepath.Join(channelFiles, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := &Platform{projectName: "demo", dataDir: dataDir, multiWorkspaceBaseDir: baseDir}
	if err := p.ensureChannelWorkspace("nex-training"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"knowledge", "memory"} {
		if info, err := os.Lstat(filepath.Join(channelFiles, name)); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s was not replaced with shared link: info=%v err=%v", name, info, err)
		}
	}
}

func TestEnsureChannelWorkspacePreservesNonEmptyLegacyDirectory(t *testing.T) {
	dataDir := t.TempDir()
	baseDir := t.TempDir()
	legacy := filepath.Join(baseDir, "nex-training", "files", "knowledge")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "keep.md"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &Platform{projectName: "demo", dataDir: dataDir, multiWorkspaceBaseDir: baseDir}
	if err := p.ensureChannelWorkspace("nex-training"); err == nil {
		t.Fatal("expected non-empty legacy directory error")
	}
	if got, err := os.ReadFile(filepath.Join(legacy, "keep.md")); err != nil || string(got) != "keep" {
		t.Fatalf("legacy content changed: %q err=%v", got, err)
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
