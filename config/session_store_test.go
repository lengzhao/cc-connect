package config

import "testing"

func TestProjectConfig_SessionStoreSettings_ChatAPIDefaults(t *testing.T) {
	proj := ProjectConfig{
		Name: "demo",
		Platforms: []PlatformConfig{
			{Type: "chat-api"},
		},
	}
	got := proj.SessionStoreSettings()
	if !got.UseJSONLChannel {
		t.Fatal("expected jsonl_channel by default for chat-api project")
	}
	if got.Dir != DefaultChatAPIRecordsDir() {
		t.Fatalf("Dir = %q, want %q", got.Dir, DefaultChatAPIRecordsDir())
	}
	if got.KeyPrefix != "chat-api:" {
		t.Fatalf("KeyPrefix = %q", got.KeyPrefix)
	}
}

func TestProjectConfig_SessionStoreSettings_NonChatAPINoDefault(t *testing.T) {
	proj := ProjectConfig{
		Name: "demo",
		Platforms: []PlatformConfig{
			{Type: "feishu"},
		},
	}
	got := proj.SessionStoreSettings()
	if got.UseJSONLChannel {
		t.Fatal("expected no jsonl default without chat-api")
	}
}

func TestProjectConfig_SessionStoreSettings_ExplicitJSON(t *testing.T) {
	proj := ProjectConfig{
		Name:         "demo",
		SessionStore: "json",
		Platforms: []PlatformConfig{
			{Type: "chat-api"},
		},
	}
	got := proj.SessionStoreSettings()
	if got.UseJSONLChannel {
		t.Fatal("explicit session_store=json should disable jsonl default")
	}
}

func TestProjectConfig_SessionStoreSettings_CustomDir(t *testing.T) {
	proj := ProjectConfig{
		Name:            "demo",
		SessionStoreDir: "/var/records",
		Platforms: []PlatformConfig{
			{Type: "chat-api"},
		},
	}
	got := proj.SessionStoreSettings()
	if !got.UseJSONLChannel {
		t.Fatal("expected jsonl default")
	}
	if got.Dir != "/var/records" {
		t.Fatalf("Dir = %q, want custom override", got.Dir)
	}
}
