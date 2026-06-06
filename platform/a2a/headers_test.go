package a2a

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
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
	params := a2asrv.NewServiceParams(map[string][]string{
		"X-Tenant-Id": {"acme"},
		"X-Trace-Id":  {"trace-1", "trace-2"},
	})
	got := collectForwardedHeaders([]string{"X-Tenant-Id", "X-Missing"}, params)
	if got["X-Tenant-Id"] != "acme" {
		t.Fatalf("tenant = %q", got["X-Tenant-Id"])
	}
	if _, ok := got["X-Missing"]; ok {
		t.Fatalf("unexpected missing header in %#v", got)
	}

	multi := collectForwardedHeaders([]string{"X-Trace-Id"}, params)
	if multi["X-Trace-Id"] != "trace-1, trace-2" {
		t.Fatalf("trace = %q", multi["X-Trace-Id"])
	}
}

func TestToCoreMessageStoresHookContextInReplyContext(t *testing.T) {
	p := &Platform{
		forwardHeaders: []string{"X-Tenant-Id", "Authorization"},
	}
	execCtx := &a2asrv.ExecutorContext{
		Message: &a2a.Message{
			ID:       "msg-1",
			Role:     a2a.MessageRoleUser,
			Metadata: map[string]any{"message_key": "message-value"},
			Parts: a2a.ContentParts{
				a2a.NewTextPart("hello"),
			},
		},
		TaskID:    "task-1",
		ContextID: "ctx-1",
		User:      &a2asrv.User{Name: "alice"},
		Metadata:  map[string]any{"request_key": "request-value"},
		ServiceParams: a2asrv.NewServiceParams(map[string][]string{
			"Authorization": {"Bearer secret"},
			"X-Tenant-Id":   {"acme"},
		}),
	}

	msg, err := p.toCoreMessage(execCtx)
	if err != nil {
		t.Fatalf("toCoreMessage() error = %v", err)
	}
	if msg.ExtraContent != "" {
		t.Fatalf("ExtraContent = %q, want empty (not forwarded to agent)", msg.ExtraContent)
	}
	rc, ok := msg.ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("ReplyCtx type = %T, want replyContext", msg.ReplyCtx)
	}
	if rc.headers["X-Tenant-Id"] != "acme" {
		t.Fatalf("headers = %#v", rc.headers)
	}
	if _, ok := rc.headers["Authorization"]; ok {
		t.Fatalf("Authorization must not be forwarded: %#v", rc.headers)
	}
	got := p.HookContext(msg.ReplyCtx)
	if got.Headers["X-Tenant-Id"] != "acme" {
		t.Fatalf("HookContext().Headers = %#v", got.Headers)
	}
	if got.Context["request_key"] != "request-value" || got.Context["message_key"] != "message-value" {
		t.Fatalf("HookContext().Context = %#v", got.Context)
	}
}

func TestJSONRPCForwardHeadersStayOutOfAgentPrompt(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"path":            "/a2a/",
		"timeout":         "2s",
		"api_token":       "secret",
		"forward_headers": []string{"X-Tenant-Id", "X-Trace-Id"},
	})
	server := httptest.NewServer(p.routes())
	defer server.Close()

	received := make(chan *core.Message, 1)
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		received <- msg
		_ = platform.Reply(context.Background(), msg.ReplyCtx, "ok")
	})

	client := newA2AClient(t, server.URL+"/a2a/", headerInterceptor{
		"Authorization": {"Bearer secret"},
		"X-A2A-User":    {"alice"},
		"X-Tenant-Id":   {"acme"},
		"X-Trace-Id":    {"trace-42"},
	})
	_, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: &a2a.Message{
			ID:        "msg-1",
			ContextID: "ctx-1",
			Role:      a2a.MessageRoleUser,
			Parts:     a2a.ContentParts{a2a.NewTextPart("hello")},
		},
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.ExtraContent != "" {
			t.Fatalf("ExtraContent = %q, want empty", msg.ExtraContent)
		}
		headers := p.HookContext(msg.ReplyCtx).Headers
		if headers["X-Tenant-Id"] != "acme" || headers["X-Trace-Id"] != "trace-42" {
			t.Fatalf("headers = %#v", headers)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler")
	}
}
