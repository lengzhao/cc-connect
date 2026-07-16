package chatapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestGetConversationDetailRequiresOwner(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	s, err := sm.NewSessionWithID("chat-api:owner", "conv1", "default")
	if err != nil {
		t.Fatalf("NewSessionWithID: %v", err)
	}
	s.AddUserHistory("hello", "owner", "Owner")
	s.AddHistory("assistant", "hi")

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+s.ID, nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "other")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

func TestGetConversationDetailReturnsView(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	s, err := sm.NewSessionWithID("chat-api:owner", "conv1", "my chat")
	if err != nil {
		t.Fatalf("NewSessionWithID: %v", err)
	}
	s.AddUserHistory("hello", "owner", "Owner")
	s.AddHistory("assistant", "world reply")

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+s.ID, nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "owner")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK   bool             `json:"ok"`
		Data conversationView `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.ID != s.ID || resp.Data.Name != "my chat" {
		t.Fatalf("data = %+v", resp.Data)
	}
	if resp.Data.LastMessagePreview != "world reply" {
		t.Fatalf("preview = %q", resp.Data.LastMessagePreview)
	}
}

func TestGenerateConversationNameAsync(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	s, err := sm.NewSessionWithID("chat-api:owner", "conv1", "default")
	if err != nil {
		t.Fatalf("NewSessionWithID: %v", err)
	}
	s.AddUserHistory("explain this code", "owner", "Owner")
	s.AddHistory("assistant", "This code implements a parser.")

	var wg sync.WaitGroup
	wg.Add(1)
	p.setHandler(func(_ core.Platform, msg *core.Message) {
		defer wg.Done()
		rc, ok := msg.ReplyCtx.(*nameReplyContext)
		if !ok {
			t.Errorf("reply ctx = %T, want *nameReplyContext", msg.ReplyCtx)
			return
		}
		if !msg.SkipHistory {
			t.Error("expected SkipHistory on name generation message")
		}
		_ = p.Reply(t.Context(), rc, "Code parser walkthrough")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+s.ID+"/name/generate", strings.NewReader(`{"force":false}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "owner")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	wg.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.GetName() == "Code parser walkthrough" {
			if got := len(s.GetHistory(0)); got != 2 {
				t.Fatalf("history length = %d, want 2 after name generation", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("name = %q, want generated title", s.GetName())
}

func TestGenerateConversationNameSkipsWhenNamedUnlessForce(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	s, err := sm.NewSessionWithID("chat-api:owner", "conv1", "manual name")
	if err != nil {
		t.Fatalf("NewSessionWithID: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+s.ID+"/name/generate", strings.NewReader(`{"force":false}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "owner")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Data.Status != "skipped" {
		t.Fatalf("status = %q, want skipped", resp.Data.Status)
	}
}

func TestAutoGenerateNameModeDefaultHeuristic(t *testing.T) {
	p := newTestPlatform(t, map[string]any{})
	if p.autoGenerateNameMode != autoGenerateNameModeHeuristic {
		t.Fatalf("mode = %q, want heuristic", p.autoGenerateNameMode)
	}
}

func TestSanitizeGeneratedName(t *testing.T) {
	got := sanitizeGeneratedName(`  "Code Review"\n  `)
	if got != "Code Review" {
		t.Fatalf("sanitize = %q", got)
	}
	long := strings.Repeat("字", 40)
	if runeLen := len([]rune(sanitizeGeneratedName(long))); runeLen > autoNameMaxRunes {
		t.Fatalf("len = %d, want <= %d", runeLen, autoNameMaxRunes)
	}
}
