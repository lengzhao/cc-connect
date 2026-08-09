package chatapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestFlatUserStore_AppendHistoryAndIndex(t *testing.T) {
	userDir := filepath.Join(t.TempDir(), "alice")
	sm := newSessionManagerForFlatUser(userDir, "alice")

	const convID = "conv_flat0123456789012345678"
	session, err := sm.NewSessionWithID("chat-api:alice", convID, "demo")
	if err != nil {
		t.Fatalf("NewSessionWithID: %v", err)
	}
	session.AddHistory("user", "hello")
	session.AddHistory("assistant", "hi")
	sm.Save()

	indexPath := filepath.Join(userDir, flatConversationsFile)
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("missing conversations.json: %v", err)
	}
	msgPath := filepath.Join(userDir, convID+flatMessageExt)
	history, err := loadHistoryJSONL(msgPath)
	if err != nil {
		t.Fatalf("loadHistoryJSONL: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history lines = %d, want 2", len(history))
	}

	reloaded := newSessionManagerForFlatUser(userDir, "alice")
	got := reloaded.FindByID(convID)
	if got == nil {
		t.Fatal("conversation not reloaded")
	}
	if len(got.GetHistory(0)) != 2 {
		t.Fatalf("reloaded history = %d, want 2", len(got.GetHistory(0)))
	}

	got.AddHistory("user", "follow-up")
	reloaded.Save()

	history2, err := loadHistoryJSONL(msgPath)
	if err != nil {
		t.Fatalf("reload jsonl: %v", err)
	}
	if len(history2) != 3 {
		t.Fatalf("appended history lines = %d, want 3", len(history2))
	}
}

func TestFlatUserStore_MigrateLegacySessionsJSON(t *testing.T) {
	userDir := filepath.Join(t.TempDir(), "bob")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(userDir, flatLegacySessionsFile)
	legacySM := core.NewSessionManager(legacyPath)
	if _, err := legacySM.NewSessionWithID("chat-api:bob", "conv_legacy0123456789012345", "old"); err != nil {
		t.Fatal(err)
	}
	legacySM.Save()

	sm := newSessionManagerForFlatUser(userDir, "bob")
	if sm.FindByID("conv_legacy0123456789012345") == nil {
		t.Fatal("legacy conversation not migrated")
	}
	if _, err := os.Stat(filepath.Join(userDir, flatConversationsFile)); err != nil {
		t.Fatal("conversations.json not created after migration")
	}
}

func TestFlatUserStore_ClearHistoryRewritesJSONL(t *testing.T) {
	userDir := filepath.Join(t.TempDir(), "dave")
	sm := newSessionManagerForFlatUser(userDir, "dave")
	const id = "conv_clear012345678901234567"
	session, err := sm.NewSessionWithID("chat-api:dave", id, "demo")
	if err != nil {
		t.Fatal(err)
	}
	session.AddHistory("user", "hello")
	session.AddHistory("assistant", "hi")
	sm.Save()

	msgPath := filepath.Join(userDir, id+flatMessageExt)
	if history, err := loadHistoryJSONL(msgPath); err != nil || len(history) != 2 {
		t.Fatalf("before clear: history = %d, err = %v", len(history), err)
	}

	session.ClearHistory()
	sm.Save()

	if _, err := os.Stat(msgPath); err == nil {
		t.Fatal("jsonl should be removed after ClearHistory")
	}

	reloaded := newSessionManagerForFlatUser(userDir, "dave")
	got := reloaded.FindByID(id)
	if got == nil {
		t.Fatal("conversation not reloaded")
	}
	if len(got.GetHistory(0)) != 0 {
		t.Fatalf("reloaded history = %d, want 0", len(got.GetHistory(0)))
	}
}

func TestFlatUserStore_DeleteRemovesJSONL(t *testing.T) {
	userDir := filepath.Join(t.TempDir(), "carol")
	sm := newSessionManagerForFlatUser(userDir, "carol")
	const id = "conv_delete0123456789012345"
	if _, err := sm.NewSessionWithID("chat-api:carol", id, "x"); err != nil {
		t.Fatal(err)
	}
	sm.FindByID(id).AddHistory("user", "x")
	sm.Save()
	msgPath := filepath.Join(userDir, id+flatMessageExt)
	if _, err := os.Stat(msgPath); err != nil {
		t.Fatal("expected jsonl before delete")
	}
	if !sm.DeleteByID(id) {
		t.Fatal("DeleteByID failed")
	}
	if _, err := os.Stat(msgPath); err == nil {
		t.Fatal("jsonl should be removed after delete")
	}
}

func TestUserShardCache_IsolatedUsers(t *testing.T) {
	base := filepath.Join(t.TempDir(), "proj", "users")
	cache := newUserShardCache(base)

	smA := cache.forUser("alice")
	smA.NewSession("chat-api:alice", "a1")
	smA.Save()

	smB := cache.forUser("bob")
	if len(smB.ListSessions("chat-api:alice")) != 0 {
		t.Fatal("bob shard must not see alice sessions")
	}

	reloaded := newUserShardCache(base)
	if len(reloaded.forUser("alice").ListSessions("chat-api:alice")) != 1 {
		t.Fatal("alice sessions not persisted")
	}
}
