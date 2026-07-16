package main

import (
	"strings"
	"testing"
)

func TestBuildCronListURL_SessionKeyScope(t *testing.T) {
	got := buildCronListURL("my-project", "chat-api:default_channel:conv_a", false)
	if !strings.Contains(got, "session_key=") {
		t.Fatalf("url = %q, want session_key query", got)
	}
	if !strings.Contains(got, "project=my-project") {
		t.Fatalf("url = %q, want project query", got)
	}
	if strings.Contains(got, "all=true") {
		t.Fatalf("url = %q, should not include all=true", got)
	}
}

func TestBuildCronListURL_AllOverridesSessionKey(t *testing.T) {
	got := buildCronListURL("my-project", "chat-api:default_channel:conv_a", true)
	if strings.Contains(got, "session_key=") {
		t.Fatalf("url = %q, all=true should omit session_key", got)
	}
	if !strings.Contains(got, "all=true") {
		t.Fatalf("url = %q, want all=true", got)
	}
}

func TestBuildCronListURL_ProjectOnly(t *testing.T) {
	got := buildCronListURL("my-project", "", false)
	if got != "/cron/list?project=my-project" {
		t.Fatalf("url = %q, want project-only list path", got)
	}
}
