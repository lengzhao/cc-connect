package core

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestJSONLChannelStore_AppendAndFold(t *testing.T) {
	dir := t.TempDir()
	channelKey := "chat-api:project-a"
	convID := "conv_test123456789012345678"

	events := []SessionRecordEvent{
		{Op: "conv_create", ConvID: convID, Name: "default", CreatedBy: "alice", CreatedAt: 100},
		{Op: "history_append", ConvID: convID, Entry: &HistoryEntry{Role: "user", Content: "hi", Timestamp: time.Unix(101, 0)}},
		{Op: "history_append", ConvID: convID, Entry: &HistoryEntry{Role: "assistant", Content: "hello", Timestamp: time.Unix(102, 0)}},
	}
	if err := AppendJSONLChannelEvents(dir, channelKey, events); err != nil {
		t.Fatalf("append: %v", err)
	}

	shards, err := LoadJSONLChannelStore(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	shard := shards[channelKey]
	if shard == nil {
		t.Fatal("missing shard for channel key")
	}
	s := shard.Sessions[convID]
	if s == nil {
		t.Fatal("missing session")
	}
	if len(s.History) != 2 {
		t.Fatalf("history len = %d, want 2", len(s.History))
	}
	if s.CreatedBy != "alice" {
		t.Fatalf("created_by = %q, want alice", s.CreatedBy)
	}
}

func TestJSONLChannelStore_ConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	channelKey := "chat-api:shared"
	st := newJSONLChannelStore(dir)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			convID := "conv_" + string(rune('a'+n%26)) + "0123456789012345678901"
			ev := SessionRecordEvent{
				Op:     "history_append",
				ConvID: convID,
				Entry:  &HistoryEntry{Role: "user", Content: "x", Timestamp: time.Now()},
			}
			_ = st.append(channelKey, []SessionRecordEvent{
				{Op: "conv_create", ConvID: convID, Name: "default", CreatedAt: time.Now().Unix()},
				ev,
			})
		}(i)
	}
	wg.Wait()

	jsonlPath, _ := st.paths(channelKey)
	info, err := os.Stat(jsonlPath)
	if err != nil {
		t.Fatalf("stat jsonl: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("jsonl file empty after concurrent append")
	}
}

func TestSessionManager_JSONLChannelSaveReload(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "legacy.json")
	storeDir := filepath.Join(dir, "records")

	sm1 := NewSessionManager(jsonPath)
	sm1.SetStoreConfig(SessionStoreConfig{
		Mode:      SessionStoreJSONLChannel,
		Dir:       storeDir,
		KeyPrefix: "chat-api:",
	})
	channelKey := "chat-api:team-a"
	s, err := sm1.NewSessionWithID(channelKey, "conv_reload0123456789012", "default")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	s.SetCreatedBy("bob")
	s.SetContextResourceVersions(map[string]string{"automon:/runtime/AUTOMON.md": "sha256-v2"})
	s.AddHistory("user", "question")
	s.AddHistory("assistant", "answer")
	sm1.Save()

	sm2 := NewSessionManager(jsonPath)
	sm2.SetStoreConfig(SessionStoreConfig{
		Mode:      SessionStoreJSONLChannel,
		Dir:       storeDir,
		KeyPrefix: "chat-api:",
	})
	got := sm2.FindByID("conv_reload0123456789012")
	if got == nil {
		t.Fatal("session not reloaded")
	}
	if len(got.GetHistory(0)) != 2 {
		t.Fatalf("history = %d, want 2", len(got.GetHistory(0)))
	}
	if version := got.GetContextResourceVersions()["automon:/runtime/AUTOMON.md"]; version != "sha256-v2" {
		t.Fatalf("context resource version = %q, want sha256-v2", version)
	}
	list := sm2.ListSessions(channelKey)
	if len(list) != 1 {
		t.Fatalf("list = %d, want 1", len(list))
	}
}
