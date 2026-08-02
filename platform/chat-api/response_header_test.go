package chatapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponseHeaderOptionFixedValue(t *testing.T) {
	got, err := responseHeaderOption(map[string]any{
		"response_header":       "X-Chat-API-Pod",
		"response_header_value": "pod-a",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.name != "X-Chat-Api-Pod" || got.value != "pod-a" {
		t.Fatalf("got = %#v", got)
	}
	if got.resolvedValue() != "pod-a" {
		t.Fatalf("resolved = %q", got.resolvedValue())
	}
}

func TestResponseHeaderOptionFromEnv(t *testing.T) {
	t.Setenv("POD_NAME", "pod-from-env")
	got, err := responseHeaderOption(map[string]any{
		"response_header":     "X-Chat-API-Pod",
		"response_header_env": "POD_NAME",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.envKey != "POD_NAME" {
		t.Fatalf("envKey = %q", got.envKey)
	}
	if got.resolvedValue() != "pod-from-env" {
		t.Fatalf("resolved = %q", got.resolvedValue())
	}
}

func TestResponseHeaderOptionFixedValueWinsOverEnv(t *testing.T) {
	t.Setenv("POD_NAME", "pod-from-env")
	got, err := responseHeaderOption(map[string]any{
		"response_header":       "X-Chat-API-Pod",
		"response_header_value": "pod-fixed",
		"response_header_env":   "POD_NAME",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.resolvedValue() != "pod-fixed" {
		t.Fatalf("resolved = %q, want pod-fixed", got.resolvedValue())
	}
}

func TestResponseHeaderOptionDisabledWhenEmpty(t *testing.T) {
	got, err := responseHeaderOption(nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.enabled() {
		t.Fatalf("got = %#v, want disabled", got)
	}
}

func TestResponseHeaderOptionRequiresValueOrEnv(t *testing.T) {
	_, err := responseHeaderOption(map[string]any{
		"response_header": "X-Chat-API-Pod",
	})
	if err == nil {
		t.Fatal("expected error when value and env both missing")
	}
}

func TestResponseHeaderOnREST(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"token":                 "secret",
		"response_header":       "X-Chat-API-Pod",
		"response_header_value": "pod-a",
	})
	sm := bindTestSessions(t, p)
	s := sm.NewSession("chat-api:user_001", "chat")

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+s.ID+"/messages?limit=10", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Chat-Api-Pod"); got != "pod-a" {
		t.Fatalf("header = %q, want pod-a", got)
	}
}

func TestResponseHeaderOnSSEChatMessages(t *testing.T) {
	t.Setenv("LOGNAME", "pod-sse")
	p := newTestPlatform(t, map[string]any{
		"token":               "secret",
		"response_header":     "X-Chat-API-Pod",
		"response_header_env": "LOGNAME",
	})
	bindTestSessions(t, p)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"hi"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Chat-Api-Pod"); got != "pod-sse" {
		t.Fatalf("header = %q, want pod-sse", got)
	}
}

func TestCORSIncludesResponseHeader(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"cors_origins":          []string{"https://app.example.com"},
		"response_header":       "X-Chat-API-Pod",
		"response_header_value": "pod-a",
	})
	req := httptest.NewRequest(http.MethodOptions, "/v1/chat-messages", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	allowed := rec.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(strings.ToLower(allowed), "x-chat-api-pod") {
		t.Fatalf("Allow-Headers = %q", allowed)
	}
	exposed := rec.Header().Get("Access-Control-Expose-Headers")
	if !strings.Contains(strings.ToLower(exposed), "x-chat-api-pod") {
		t.Fatalf("Expose-Headers = %q", exposed)
	}
}
