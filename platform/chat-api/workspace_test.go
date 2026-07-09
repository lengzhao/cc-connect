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
	if got, err := p.ResolveChannelName(defaultWorkspaceChannelID); err != nil || got != "." {
		t.Fatalf("default channel name = %q, %v; want .", got, err)
	}
	if got, err := p.ResolveChannelName("team/backend"); err != nil || got != "" {
		t.Fatalf("explicit channel name = %q, %v; want empty", got, err)
	}
}

func TestChatMessagesUsesDefaultWorkspaceChannelWhenHeaderOmitted(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"token": "secret",
	})
	sm := bindTestSessions(t, p)
	_ = sm.NewSession("chat-api:user_001", "default")

	var gotChannel string
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		gotChannel = msg.ChannelKey
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
}
