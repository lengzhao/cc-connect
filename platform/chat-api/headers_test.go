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

func TestNormalizeForwardHeaderNames(t *testing.T) {
	got := normalizeForwardHeaderNames([]string{
		" x-tenant-id ",
		"X-Tenant-Id",
		"Authorization",
		"Cookie",
		"",
		"X-Trace-Id",
	})
	if len(got) != 2 {
		t.Fatalf("got = %#v, want 2 headers", got)
	}
	if got[0] != "X-Tenant-Id" || got[1] != "X-Trace-Id" {
		t.Fatalf("got = %#v", got)
	}
}

func TestCollectForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	req.Header.Add("X-Trace-Id", "trace-1")
	req.Header.Add("X-Trace-Id", "trace-2")
	req.Header.Set("Authorization", "Bearer secret")

	got := collectForwardedHeaders([]string{"X-Tenant-Id", "X-Missing", "Authorization"}, req)
	if got["X-Tenant-Id"] != "acme" {
		t.Fatalf("tenant = %q", got["X-Tenant-Id"])
	}
	if _, ok := got["X-Missing"]; ok {
		t.Fatalf("unexpected missing header in %#v", got)
	}
	if _, ok := got["Authorization"]; ok {
		t.Fatalf("Authorization must not be forwarded: %#v", got)
	}

	multi := collectForwardedHeaders([]string{"X-Trace-Id"}, req)
	if multi["X-Trace-Id"] != "trace-1, trace-2" {
		t.Fatalf("trace = %q", multi["X-Trace-Id"])
	}
}

func TestHookContextHeadersAndMetadata(t *testing.T) {
	p := newTestPlatform(t, nil)
	rc := &replyContext{
		metadata: map[string]any{"tenant": "acme"},
		headers:  map[string]string{"X-Trace-Id": "trace-1"},
	}
	got := p.HookContext(rc)
	if got.Context["tenant"] != "acme" {
		t.Fatalf("context = %#v", got.Context)
	}
	if got.Headers["X-Trace-Id"] != "trace-1" {
		t.Fatalf("headers = %#v", got.Headers)
	}
}

func TestChatMessagesForwardHeadersStayOutOfAgentPrompt(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"token":           "secret",
		"forward_headers": []string{"X-Tenant-Id", "X-Trace-Id", "Authorization"},
	})
	bindTestSessions(t, p)

	var (
		mu      sync.Mutex
		headers map[string]string
		ctx     map[string]any
		agent   core.AgentContext
		extra   string
		done    = make(chan struct{})
	)
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		mu.Lock()
		hc := platform.(core.HookContextProvider).HookContext(msg.ReplyCtx)
		headers = hc.Headers
		ctx = hc.Context
		agent = msg.AgentContext.Clone()
		extra = msg.ExtraContent
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
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Tenant-Id", "acme")
	req.Header.Set("X-Trace-Id", "trace-42")

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
	if headers["X-Tenant-Id"] != "acme" || headers["X-Trace-Id"] != "trace-42" {
		t.Fatalf("headers = %#v", headers)
	}
	if _, ok := headers["Authorization"]; ok {
		t.Fatalf("Authorization must not be forwarded: %#v", headers)
	}
	if ctx["hook_only"] != "yes" {
		t.Fatalf("context = %#v", ctx)
	}
	if extra != "" {
		t.Fatalf("ExtraContent = %q, want empty", extra)
	}
	if agent.Language != "" || agent.TaskID != "" || len(agent.Custom) != 0 {
		t.Fatalf("AgentContext must stay empty without agent_context_headers: %+v", agent)
	}
}

func TestCORSIncludesForwardHeaders(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"cors_origins":    []string{"https://app.example.com"},
		"forward_headers": []string{"X-Tenant-Id", "X-Trace-Id"},
	})
	req := httptest.NewRequest(http.MethodOptions, "/v1/chat-messages", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	allowed := rec.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(allowed, "X-Tenant-Id") || !strings.Contains(allowed, "X-Trace-Id") {
		t.Fatalf("Allow-Headers = %q", allowed)
	}
}
