package chatapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoggingHTTPRequestResponse(t *testing.T) {
	prev := slog.Default()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p := newTestPlatform(t, map[string]any{
		"api_token": "secret-token",
		"agent_context_headers": map[string]string{
			"trace_id": "X-Trace-ID",
			"task_id":  "X-Task-ID",
		},
	})

	handler := p.loggingHTTP(func(w http.ResponseWriter, r *http.Request) {
		writeOK(w, http.StatusOK, map[string]string{"echo": "hi"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations", strings.NewReader(`{"name":"demo"}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Chat-API-User", "local_user")
	req.Header.Set("X-Chat-API-Channel", "local-workspace")
	req.Header.Set("X-Trace-ID", "trace-abc")
	req.Header.Set("X-Task-ID", "task-123")
	rec := httptest.NewRecorder()

	handler(rec, req)

	out := buf.String()
	if !strings.Contains(out, "chat-api: http request") {
		t.Fatalf("expected request log, got:\n%s", out)
	}
	if !strings.Contains(out, "chat-api: http response") {
		t.Fatalf("expected response log, got:\n%s", out)
	}
	if strings.Contains(out, "secret-token") {
		t.Fatalf("token must not appear in log, got:\n%s", out)
	}
	if strings.Contains(out, "Authorization") {
		t.Fatalf("authorization header must not be logged, got:\n%s", out)
	}
	if !strings.Contains(out, "user=local_user") {
		t.Fatalf("expected user in log, got:\n%s", out)
	}
	if !strings.Contains(out, "channel=local-workspace") {
		t.Fatalf("expected channel in log, got:\n%s", out)
	}
	if !strings.Contains(out, "trace_id=trace-abc") {
		t.Fatalf("expected trace_id in log, got:\n%s", out)
	}
	if !strings.Contains(out, "task=task-123") {
		t.Fatalf("expected task in log, got:\n%s", out)
	}
	if !strings.Contains(out, "duration_ms") {
		t.Fatalf("expected duration_ms in response log, got:\n%s", out)
	}
	if !strings.Contains(out, "status=200") {
		t.Fatalf("expected status in response log, got:\n%s", out)
	}
	if strings.Contains(out, `"echo":"hi"`) || strings.Contains(out, `"echo": "hi"`) {
		t.Fatalf("response body must not be logged, got:\n%s", out)
	}
	if !strings.Contains(out, `name`) || !strings.Contains(out, `demo`) {
		t.Fatalf("expected request body in log, got:\n%s", out)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHTTPLogContextAttrsReadsConfiguredHeaders(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"user_header":    "X-Tenant-User",
		"channel_header": "X-Workspace-Id",
		"agent_context_headers": map[string]string{
			"trace_id": "X-Request-Trace",
			"task_id":  "X-Job-Id",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", nil)
	req.Header.Set("X-Tenant-User", "u1")
	req.Header.Set("X-Workspace-Id", "ws1")
	req.Header.Set("X-Request-Trace", "t1")
	req.Header.Set("X-Job-Id", "j1")

	attrs := p.httpLogContextAttrs(req)
	want := []any{
		"user", "u1",
		"channel", "ws1",
		"trace_id", "t1",
		"task", "j1",
	}
	if len(attrs) != len(want) {
		t.Fatalf("attrs = %v, want %v", attrs, want)
	}
	for i := range want {
		if attrs[i] != want[i] {
			t.Fatalf("attrs[%d] = %v, want %v (full attrs=%v)", i, attrs[i], want[i], attrs)
		}
	}
}

func TestHTTPLogContextAttrsFromQueryUser(t *testing.T) {
	p := newTestPlatform(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/conversations?user=query_user", nil)
	attrs := p.httpLogContextAttrs(req)
	if len(attrs) != 2 || attrs[0] != "user" || attrs[1] != "query_user" {
		t.Fatalf("attrs = %v, want user=query_user", attrs)
	}
}

func TestLoggingHTTPOmitsAuthorization(t *testing.T) {
	prev := slog.Default()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p := newTestPlatform(t, nil)
	handler := p.loggingHTTP(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations", nil)
	req.Header.Set("Authorization", "Bearer top-secret")
	rec := httptest.NewRecorder()
	handler(rec, req)

	out := buf.String()
	if strings.Contains(out, "top-secret") || strings.Contains(out, "Authorization") {
		t.Fatalf("authorization must not be logged, got:\n%s", out)
	}
}

func TestTruncateHTTPBodyForLog(t *testing.T) {
	got := truncateHTTPBodyForLog(strings.Repeat("a", 10), 5)
	want := "aaaaa...(truncated)"
	if got != want {
		t.Fatalf("truncateHTTPBodyForLog = %q, want %q", got, want)
	}
	got = truncateHTTPBodyForLog(strings.Repeat("b", maxLogRequestBodyBytes+1), maxLogRequestBodyBytes)
	if len(got) != maxLogRequestBodyBytes+len("...(truncated)") {
		t.Fatalf("truncated len = %d, want %d", len(got), maxLogRequestBodyBytes+len("...(truncated)"))
	}
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("expected truncation suffix, got %q", got)
	}
}
