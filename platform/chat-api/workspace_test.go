package chatapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestChannelKeyForMessage(t *testing.T) {
	p := &Platform{}
	if got := p.channelKeyForMessage(""); got != defaultWorkspaceChannelID {
		t.Fatalf("empty header = %q, want %q", got, defaultWorkspaceChannelID)
	}
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

func TestChatMessagesUsesDefaultWorkspaceChannelWhenHeaderOmitted(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"token": "secret",
	})
	sm := bindTestSessions(t, p)
	_ = sm.NewSession("chat-api:user_001", "default")

	var gotChannel string
	var gotSessionKey string
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		gotChannel = msg.ChannelKey
		gotSessionKey = msg.SessionKey
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = card.Finalize(context.Background(), "ok")
		}
	})

	body := `{"query":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotChannel != defaultWorkspaceChannelID {
		t.Fatalf("ChannelKey = %q, want %q", gotChannel, defaultWorkspaceChannelID)
	}
	if !strings.HasPrefix(gotSessionKey, "chat-api:"+defaultWorkspaceChannelID+":") {
		t.Fatalf("SessionKey = %q, want prefix chat-api:%s:", gotSessionKey, defaultWorkspaceChannelID)
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
