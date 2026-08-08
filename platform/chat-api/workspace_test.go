package chatapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChannelKeyForMessage(t *testing.T) {
	p := &Platform{}
	if got := p.channelKeyForMessage("team/backend"); got != "team/backend" {
		t.Fatalf("explicit channel = %q", got)
	}
}

func TestResolveChannelName(t *testing.T) {
	p := &Platform{}
	if got, err := p.ResolveChannelName(defaultWorkspaceChannelID); err != nil || got != defaultWorkspaceChannelID {
		t.Fatalf("default channel name = %q, %v; want %q", got, err, defaultWorkspaceChannelID)
	}
	if got, err := p.ResolveChannelName("chat-123"); err != nil || got != "chat-123" {
		t.Fatalf("explicit channel name = %q, %v; want chat-123", got, err)
	}
	if got, err := p.ResolveChannelName("team/backend"); err != nil || got != "team/backend" {
		t.Fatalf("nested channel name = %q, %v; want team/backend", got, err)
	}
	if got, err := p.ResolveChannelName("../escape"); err != nil || got != "" {
		t.Fatalf("unsafe channel name = %q, %v; want empty", got, err)
	}
}

func TestIsSafeWorkspaceChannelPath(t *testing.T) {
	valid := []string{"chat-123", "team-alpha/backend", "a.b", "repo_v2", defaultWorkspaceChannelID}
	for _, ch := range valid {
		if !isSafeWorkspaceChannelPath(ch) {
			t.Fatalf("%q should be valid", ch)
		}
	}
	invalid := []string{"", ".", "..", "/abs", "rel/", "a//b", "a/../b", "seg/.."}
	for _, ch := range invalid {
		if isSafeWorkspaceChannelPath(ch) {
			t.Fatalf("%q should be invalid", ch)
		}
	}
}

func TestChatMessagesRequiresChannel(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"token": "secret",
	})
	bindTestSessions(t, p)

	body := `{"query":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "channel required") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestResolveChannelRejectsEmpty(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"hi"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	w := httptest.NewRecorder()
	got, ok := p.resolveChannel(w, req)
	if ok || got != "" || w.Code != http.StatusBadRequest {
		t.Fatalf("empty channel: got=%q ok=%v status=%d, want reject 400", got, ok, w.Code)
	}
}

func TestResolveChannelRejectsInvalidNames(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	invalid := []string{"..", "/abs", "a/../b", "rel/", "a//b", "bad channel"}
	for _, ch := range invalid {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"hi"}`))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("X-Chat-API-User", "user_001")
		req.Header.Set("X-Chat-API-Channel", ch)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		got, ok := p.resolveChannel(w, req)
		if ok || got != "" || w.Code != http.StatusBadRequest {
			t.Fatalf("channel %q: got=%q ok=%v status=%d, want reject 400", ch, got, ok, w.Code)
		}
	}
}
