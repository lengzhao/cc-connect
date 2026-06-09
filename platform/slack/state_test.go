package slack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveSlackStatePath(t *testing.T) {
	got := resolveSlackStatePath(map[string]any{
		"cc_data_dir": "/tmp/data",
		"cc_project":  "my/project",
	})
	want := filepath.Join("/tmp/data", "slack", "my_project", "state.json")
	if got != want {
		t.Fatalf("resolveSlackStatePath() = %q, want %q", got, want)
	}

	override := resolveSlackStatePath(map[string]any{
		"state_dir": "/custom/state",
	})
	if override != filepath.Join("/custom/state", "state.json") {
		t.Fatalf("state_dir override = %q", override)
	}
	if resolveSlackStatePath(map[string]any{}) != "" {
		t.Fatal("expected empty path without data dir and project")
	}
}

func TestNewPlatform_DefaultTTLAndStatePath(t *testing.T) {
	plat, err := New(map[string]any{
		"bot_token":   "xoxb-test",
		"app_token":   "xapp-test",
		"cc_data_dir": "/tmp/data",
		"cc_project":  "demo",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)
	if p.threadActiveTTL != defaultThreadActiveTTL {
		t.Fatalf("threadActiveTTL = %s, want %s", p.threadActiveTTL, defaultThreadActiveTTL)
	}
	if p.dedupTTL != defaultInboundDedupTTL {
		t.Fatalf("dedupTTL = %s, want %s", p.dedupTTL, defaultInboundDedupTTL)
	}
	wantPath := filepath.Join("/tmp/data", "slack", "demo", "state.json")
	if p.statePath != wantPath {
		t.Fatalf("statePath = %q, want %q", p.statePath, wantPath)
	}
}

func TestThreadState_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "slack-state")
	statePath := filepath.Join(stateDir, "state.json")

	p1 := &Platform{
		statePath:       statePath,
		threadActiveTTL: defaultThreadActiveTTL,
		activeThreads:   make(map[string]time.Time),
	}
	p1.markActiveThread("C123", "1717000000.000100")

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var snap slackThreadStateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if len(snap.ActiveThreads) != 1 {
		t.Fatalf("active_threads len = %d, want 1", len(snap.ActiveThreads))
	}

	p2 := &Platform{
		statePath:       statePath,
		threadActiveTTL: defaultThreadActiveTTL,
		activeThreads:   make(map[string]time.Time),
	}
	p2.loadSlackThreadState()
	if !p2.isActiveThread("C123", "1717000000.000100") {
		t.Fatal("expected active thread to reload from disk")
	}
}

func TestThreadState_ExpiredEntriesDroppedOnLoad(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	old := time.Now().Add(-100 * time.Hour)
	snap := slackThreadStateSnapshot{
		Version: slackStateVersion,
		ActiveThreads: map[string]time.Time{
			"C1:111.000": old,
		},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(statePath, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	p := &Platform{
		statePath:       statePath,
		threadActiveTTL: defaultThreadActiveTTL,
		activeThreads:   make(map[string]time.Time),
	}
	p.loadSlackThreadState()
	if p.isActiveThread("C1", "111.000") {
		t.Fatal("expired thread should not be active after load")
	}
}

func TestThreadState_StopPersists(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	p := &Platform{
		statePath:       statePath,
		threadActiveTTL: defaultThreadActiveTTL,
		activeThreads:   make(map[string]time.Time),
	}
	p.markActiveThread("C9", "999.000")
	if err := os.Remove(statePath); err != nil {
		t.Fatalf("remove state: %v", err)
	}
	p.threadStateMu.Lock()
	p.activeThreads["C9:999.000"] = time.Now()
	p.threadStateMu.Unlock()

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("expected state file after Stop(): %v", err)
	}
}

func TestIsDuplicateInbound_TTLExpires(t *testing.T) {
	p := &Platform{
		dedupTTL:      20 * time.Millisecond,
		recentInbound: make(map[string]time.Time),
	}
	if p.isDuplicateInbound("C1", "1.000") {
		t.Fatal("first inbound should not be duplicate")
	}
	if !p.isDuplicateInbound("C1", "1.000") {
		t.Fatal("second inbound with same ts should be duplicate")
	}
	time.Sleep(30 * time.Millisecond)
	if p.isDuplicateInbound("C1", "1.000") {
		t.Fatal("duplicate should expire after TTL")
	}
}
