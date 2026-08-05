package chatapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDebugUIDisabledByDefault(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	req := httptest.NewRequest(http.MethodGet, "/debug/", nil)
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when debug_ui disabled", rec.Code)
	}
}

func TestDebugUIServesPage(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"token":    "secret",
		"debug_ui": true,
		"path":     "/v1/",
	})
	req := httptest.NewRequest(http.MethodGet, "/debug/", nil)
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	html := body
	if !strings.Contains(html, "chat-api") {
		t.Fatalf("missing page title/content")
	}
	if !strings.Contains(html, "window.__CHAT_API_CONFIG__") || !strings.Contains(html, `"/v1/"`) {
		snippet := html
		if len(snippet) > 800 {
			snippet = snippet[700:800]
		}
		t.Fatalf("missing injected api config near: %s", snippet)
	}
	if !strings.Contains(html, "let lastStreamBubble = null") ||
		!strings.Contains(html, "function streamBubble(role, key, tag)") {
		t.Fatalf("debug UI should render stream bubbles lazily and in event order")
	}
	if strings.Contains(html, `const thinkingBody = addBubble("thinking", "", "thinking")`) ||
		strings.Contains(html, `const answerBody = addBubble("assistant", "", "assistant")`) {
		t.Fatalf("debug UI must not pre-create thinking or answer bubbles")
	}
	if !strings.Contains(html, "Ensure bubble exists first") {
		t.Fatalf("debug UI must create assistant/thinking bubble before appending deltas")
	}
	if !strings.Contains(html, "AgentContext headers") {
		t.Fatalf("debug UI must always expose AgentContext headers section")
	}
	if !strings.Contains(html, `id="customHeader1Name"`) ||
		!strings.Contains(html, `id="customHeader2Name"`) ||
		!strings.Contains(html, "customHeaderSlots") {
		t.Fatalf("debug UI must expose two customizable AgentContext header slots")
	}
	if !strings.Contains(html, "card_group") || !strings.Contains(html, "showIxModal") {
		t.Fatalf("debug UI must honor question_request.card_group for question UI")
	}
	if !strings.Contains(html, "/conversations/messages/respond") ||
		!strings.Contains(html, "eventActionLabel") ||
		!strings.Contains(html, "knownAskEvent") ||
		!strings.Contains(html, "optionDisplayText") ||
		!strings.Contains(html, `choice.type = multiSelect ? "checkbox" : "radio"`) ||
		!strings.Contains(html, `btn.textContent = "Commit"`) {
		t.Fatalf("debug UI must support card contract respond path, known ask events, and rich options")
	}
	if !strings.Contains(html, `input[type="radio"], input[type="checkbox"]`) ||
		!strings.Contains(html, "flex: 0 0 auto") {
		t.Fatalf("debug UI choice controls must not inherit full-width text input styles")
	}
	if !strings.Contains(html, `dataset.kind = "custom"`) ||
		!strings.Contains(html, "其他…自己输入") ||
		!strings.Contains(html, "custom_input: customInput") {
		t.Fatalf("debug UI must expose allow_custom_input as a radio/checkbox option whose value is the typed text")
	}
	if !strings.Contains(html, `id="btnIxClose"`) || !strings.Contains(html, "unlockComposer") {
		t.Fatalf("debug UI must allow hiding confirmation modal to send a parallel chat-messages")
	}
	if !strings.Contains(html, "activeRunId") ||
		!strings.Contains(html, "resumeActiveRun") ||
		!strings.Contains(html, "resumeRunId") ||
		!strings.Contains(html, "自动重连") {
		t.Fatalf("debug UI must persist activeRunId and auto-resume after refresh")
	}
	// Debug page itself must not require auth (otherwise hard to open).
	if rec.Header().Get("Content-Type") == "" || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}

	req2 := httptest.NewRequest(http.MethodGet, "/debug/config.json", nil)
	rec2 := httptest.NewRecorder()
	p.routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("config status = %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), `"api_path":"/v1/"`) {
		t.Fatalf("config body = %s", rec2.Body.String())
	}
}

func TestDebugUIUsesCustomHeaders(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"token":            "secret",
		"debug_ui":         true,
		"path":             "/api/",
		"user_header":      "X-Custom-User",
		"user_name_header": "X-Custom-User-Name",
		"channel_header":   "X-Custom-Channel",
		"agent_context_headers": map[string]any{
			"language":         "X-App-Lang",
			"task_id":          "X-App-Task",
			"custom.tenant_id": "X-App-Tenant",
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/debug/", nil)
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{
		"X-Custom-User",
		"X-Custom-User-Name",
		"X-Custom-Channel (optional)",
		"X-App-Lang",
		"X-App-Task",
		"X-App-Tenant",
		`"userHeader":"X-Custom-User"`,
		`"userNameHeader":"X-Custom-User-Name"`,
		`"channelHeader":"X-Custom-Channel"`,
		`"headerName":"X-App-Lang"`,
		`"apiPath":"/api/"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in debug page", want)
		}
	}
	for _, omit := range []string{"X-Chat-API-User", "X-Trace-ID", "traceId"} {
		if strings.Contains(body, omit) {
			t.Fatalf("unexpected default header %q in customized debug page", omit)
		}
	}

	req2 := httptest.NewRequest(http.MethodGet, "/debug/config.json", nil)
	rec2 := httptest.NewRecorder()
	p.routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("config status = %d", rec2.Code)
	}
	cfg := rec2.Body.String()
	if !strings.Contains(cfg, `"user_name_header":"X-Custom-User-Name"`) {
		t.Fatalf("config body = %s", cfg)
	}
}

func TestDebugUIAgentContextFields(t *testing.T) {
	fields := debugUIAgentContextFields(nil)
	if len(fields) != 4 {
		t.Fatalf("default fields = %d, want 4", len(fields))
	}
	if fields[0].HeaderName != "X-Language" || fields[0].InputID != "lang" {
		t.Fatalf("default language field = %#v", fields[0])
	}

	custom := debugUIAgentContextFields(agentContextHeaderMap{
		"trace_id":         "X-Req-Trace",
		"custom.tenant_id": "X-Req-Tenant",
	})
	if len(custom) != 2 {
		t.Fatalf("custom fields = %#v", custom)
	}
	if custom[0].HeaderName != "X-Req-Tenant" || custom[0].InputID != "tenantId" {
		t.Fatalf("sorted custom[0] = %#v", custom[0])
	}
	if custom[1].HeaderName != "X-Req-Trace" || custom[1].InputID != "traceId" {
		t.Fatalf("sorted custom[1] = %#v", custom[1])
	}
}

func TestBuildDebugUIPage(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "debug_ui": true})
	page, err := p.buildDebugUIPage()
	if err != nil {
		t.Fatalf("buildDebugUIPage: %v", err)
	}
	if len(page) == 0 {
		t.Fatal("empty page")
	}
	if !strings.Contains(string(page), "Ensure bubble exists first") {
		t.Fatalf("rendered page missing stream bubble logic")
	}
}
