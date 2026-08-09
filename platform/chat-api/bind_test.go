package chatapi

import (
	"path/filepath"
	"testing"
)

func TestSessionUsersBaseDir(t *testing.T) {
	got := sessionUsersBaseDir("/data", "sit_communication_agent")
	want := filepath.Join("/data", "sessions", "sit_communication_agent", "users")
	if got != want {
		t.Fatalf("sessionUsersBaseDir() = %q, want %q", got, want)
	}
}

func TestSessionStoreConfigFromOpts_DefaultShardBase(t *testing.T) {
	base, legacy := sessionStoreConfigFromOpts(map[string]any{
		"cc_data_dir": "/data",
		"cc_project":  "demo",
	})
	if legacy != "" {
		t.Fatalf("legacy = %q, want empty", legacy)
	}
	want := filepath.Join("/data", "sessions", "demo", "users")
	if base != want {
		t.Fatalf("base = %q, want %q", base, want)
	}
}

func TestSessionStoreConfigFromOpts_LegacyFile(t *testing.T) {
	base, legacy := sessionStoreConfigFromOpts(map[string]any{
		"session_store": "/data/sessions/demo.json",
	})
	if base != "" {
		t.Fatalf("base = %q, want empty", base)
	}
	if legacy != "/data/sessions/demo.json" {
		t.Fatalf("legacy = %q", legacy)
	}
}

func TestPlatform_SessionsForUser_ShardsByUser(t *testing.T) {
	dataDir := t.TempDir()
	p := newTestPlatform(t, map[string]any{
		"cc_data_dir": dataDir,
		"cc_project":  "demo",
	})
	sm := p.sessionsForUser("alice")
	sm.NewSession("chat-api:alice", "one")
	sm.Save()

	other := p.sessionsForUser("bob")
	if len(other.ListSessions("chat-api:alice")) != 0 {
		t.Fatal("bob shard must not see alice sessions")
	}

	reloaded := newTestPlatform(t, map[string]any{
		"cc_data_dir": dataDir,
		"cc_project":  "demo",
	})
	if len(reloaded.sessionsForUser("alice").ListSessions("chat-api:alice")) != 1 {
		t.Fatal("alice sessions not reloaded from shard file")
	}
}
