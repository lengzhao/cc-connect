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
	if !strings.Contains(html, `window.__CHAT_API_PATH__=`) || !strings.Contains(html, `"/v1/"`) {
		snippet := html
		if len(snippet) > 800 {
			snippet = snippet[700:800]
		}
		t.Fatalf("missing injected api path near: %s", snippet)
	}
	if !strings.Contains(html, "let lastStreamBubble = null") ||
		!strings.Contains(html, "function streamBubble(role, key, tag)") {
		t.Fatalf("debug UI should render stream bubbles lazily and in event order")
	}
	if strings.Contains(html, `const thinkingBody = addBubble("thinking", "", "thinking")`) ||
		strings.Contains(html, `const answerBody = addBubble("assistant", "", "assistant")`) {
		t.Fatalf("debug UI must not pre-create thinking or answer bubbles")
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
