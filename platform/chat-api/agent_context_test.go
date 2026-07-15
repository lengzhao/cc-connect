package chatapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestAgentContextHeadersOption(t *testing.T) {
	m, err := agentContextHeadersOption(map[string]any{
		"agent_context_headers": map[string]any{
			"language":         "X-Language",
			"task_id":          "x-task-id",
			"custom.tenant_id": "X-Tenant-ID",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["language"] != "X-Language" || m["task_id"] != "X-Task-Id" || m["custom.tenant_id"] != "X-Tenant-Id" {
		t.Fatalf("map = %#v", m)
	}

	_, err = agentContextHeadersOption(map[string]any{
		"agent_context_headers": map[string]any{
			"language": "Authorization",
		},
	})
	if err == nil {
		t.Fatal("expected blocked header error")
	}

	_, err = agentContextHeadersOption(map[string]any{
		"agent_context_headers": map[string]any{
			"unknown": "X-Foo",
		},
	})
	if err == nil {
		t.Fatal("expected unsupported field error")
	}
}

func TestCollectAgentContextFromHeaders(t *testing.T) {
	m := agentContextHeaderMap{
		"language":         "X-Language",
		"task_id":          "X-Task-Id",
		"trace_id":         "X-Trace-Id",
		"custom.tenant_id": "X-Tenant-Id",
	}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("x-language", "zh") // case-insensitive
	req.Header.Set("X-Task-Id", "task-9")
	req.Header.Set("X-Trace-Id", "tr-1")
	req.Header.Set("X-Tenant-Id", "acme")
	req.Header.Set("X-Ignored", "nope")

	got := m.collectAgentContext(req)
	if got.Language != "zh" || got.TaskID != "task-9" || got.TraceID != "tr-1" {
		t.Fatalf("standard fields = %+v", got)
	}
	if got.Custom["custom.tenant_id"] != "acme" {
		t.Fatalf("custom = %#v", got.Custom)
	}
}

func TestChatMessagesPassesAgentContext(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"token": "secret",
		"agent_context_headers": map[string]any{
			"language":         "X-Language",
			"task_id":          "X-Task-ID",
			"custom.tenant_id": "X-Tenant-ID",
		},
	})
	bindTestSessions(t, p)

	var (
		mu   sync.Mutex
		got  core.AgentContext
		done = make(chan struct{})
	)
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		mu.Lock()
		got = msg.AgentContext.Clone()
		mu.Unlock()
		close(done)
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = card.Finalize(context.Background(), "ok")
		}
	})

	body := `{"query":"hello","metadata":{"hook_only":"yes"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Language", "ja")
	req.Header.Set("X-Task-ID", "job-7")
	req.Header.Set("X-Tenant-ID", "acme")
	req.Header.Set("X-Unknown", "ignored")

	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called")
	}

	mu.Lock()
	defer mu.Unlock()
	if got.Language != "ja" || got.TaskID != "job-7" {
		t.Fatalf("AgentContext = %+v", got)
	}
	if got.Custom["custom.tenant_id"] != "acme" {
		t.Fatalf("custom = %#v", got.Custom)
	}

	// metadata remains hooks-only — ReplyCtx still carries it, AgentContext does not.
	rc := replyContext{metadata: map[string]any{"hook_only": "yes"}}
	hookCtx := p.HookContext(rc)
	if hookCtx.Context["hook_only"] != "yes" {
		t.Fatalf("hooks metadata broken: %#v", hookCtx.Context)
	}
}

func TestCORSIncludesAgentContextHeaders(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"cors_origins": []string{"https://app.example.com"},
		"agent_context_headers": map[string]any{
			"language": "X-Language",
			"task_id":  "X-Task-ID",
		},
	})
	req := httptest.NewRequest(http.MethodOptions, "/v1/chat-messages", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	allowed := rec.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(allowed, "X-Language") || !strings.Contains(allowed, "X-Task-Id") {
		t.Fatalf("Allow-Headers = %q", allowed)
	}
}
