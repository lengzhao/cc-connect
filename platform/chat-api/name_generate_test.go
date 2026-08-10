package chatapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

	handlerCalled := make(chan struct{}, 1)
	p.setHandler(func(_ core.Platform, msg *core.Message) {
		handlerCalled <- struct{}{}
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+s.ID+"/name/generate", strings.NewReader(`{"force":false}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "owner")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.GetName() == "explain this code" {
			if got := len(s.GetHistory(0)); got != 2 {
				t.Fatalf("history length = %d, want 2 after name generation", got)
			}
			select {
			case <-handlerCalled:
				t.Fatal("name generation must not call Agent handler")
			default:
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("name = %q, want heuristic title", s.GetName())
}

func TestGenerateConversationNameUsesDedicatedModel(t *testing.T) {
	modelReady := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		modelReady <- body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"轻量代码会话"}}]}`))
	}))
	defer server.Close()

	p := newTestPlatform(t, map[string]any{
		"token":         "secret",
		"name_api_key":  "provider-key",
		"name_base_url": server.URL,
		"name_model":    "cheap-model",
	})
	sm := bindTestSessions(t, p)
	s, err := sm.NewSessionWithID("chat-api:owner", "conv1", "default")
	if err != nil {
		t.Fatalf("NewSessionWithID: %v", err)
	}
	s.AddUserHistory("hello", "owner", "Owner")
	s.AddHistory("assistant", "world")

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+s.ID+"/name/generate", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "owner")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.GetName() == "轻量代码会话" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var gotModel string
	select {
	case gotModel = <-modelReady:
	case <-time.After(2 * time.Second):
		t.Fatal("dedicated model request was not received")
	}
	if s.GetName() != "轻量代码会话" || gotModel != "cheap-model" {
		t.Fatalf("name = %q, model = %q", s.GetName(), gotModel)
	}
}

func TestGenerateConversationNameFallsBackToHeuristicWhenProviderFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "provider unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	p := newTestPlatform(t, map[string]any{
		"token":         "secret",
		"name_api_key":  "provider-key",
		"name_base_url": server.URL,
		"name_model":    "cheap-model",
	})
	sm := bindTestSessions(t, p)
	s, err := sm.NewSessionWithID("chat-api:owner", "conv1", "default")
	if err != nil {
		t.Fatalf("NewSessionWithID: %v", err)
	}
	s.AddUserHistory("explain failing provider", "owner", "Owner")
	s.AddHistory("assistant", "world")

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+s.ID+"/name/generate", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "owner")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.GetName() == "explain failing provider" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("name = %q, want heuristic title", s.GetName())
}

func TestGenerateConversationNameUsesClaudeMessagesAPI(t *testing.T) {
	requestReady := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q, want /v1/messages", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "provider-key" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version = %q", r.Header.Get("anthropic-version"))
		}
		requestReady <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"Claude session"}]}`))
	}))
	defer server.Close()

	p := newTestPlatform(t, map[string]any{
		"token":         "secret",
		"name_type":     "claude",
		"name_api_key":  "provider-key",
		"name_base_url": server.URL,
		"name_model":    "claude-haiku",
	})
	sm := bindTestSessions(t, p)
	s, err := sm.NewSessionWithID("chat-api:owner", "conv1", "default")
	if err != nil {
		t.Fatalf("NewSessionWithID: %v", err)
	}
	s.AddUserHistory("hello", "owner", "Owner")
	s.AddHistory("assistant", "world")

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/"+s.ID+"/name/generate", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "owner")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	select {
	case <-requestReady:
	case <-time.After(2 * time.Second):
		t.Fatal("claude request was not received")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.GetName() == "Claude session" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("name = %q, want Claude session", s.GetName())
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
	if p.nameProviderType != defaultNameProviderType {
		t.Fatalf("provider type = %q, want %q", p.nameProviderType, defaultNameProviderType)
	}
}

func TestShouldGenerateAINameSkipsShortInput(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"auto_generate_name_mode": "ai"})
	if p.shouldGenerateAIName("你好") {
		t.Fatal("short input should skip AI name generation")
	}
	if !p.shouldGenerateAIName("请帮我解释这段代码") {
		t.Fatal("long input should generate AI name")
	}
}

func TestAutoNameAIModeAppliesHeuristicFirstThenOverwrites(t *testing.T) {
	nameReady := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nameReady <- struct{}{}
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"AI 生成的标题"}}]}`))
	}))
	defer server.Close()

	p := newTestPlatform(t, map[string]any{
		"token":                   "secret",
		"auto_generate_name_mode": "ai",
		"name_api_key":            "provider-key",
		"name_base_url":           server.URL,
		"name_model":              "cheap-model",
	})
	sm := bindTestSessions(t, p)
	s, err := sm.NewSessionWithID("chat-api:owner", "conv1", "default")
	if err != nil {
		t.Fatalf("NewSessionWithID: %v", err)
	}

	query := "请帮我解释这段代码的结构"
	p.startAutoNameGeneration(newNameRunID(), s, sm, query)

	if got := s.GetName(); got != autoNameFromQuery(query) {
		t.Fatalf("immediate name = %q, want heuristic %q", got, autoNameFromQuery(query))
	}

	select {
	case <-nameReady:
	case <-time.After(2 * time.Second):
		t.Fatal("provider request was not received")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.GetName() == "AI 生成的标题" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("name = %q, want AI title", s.GetName())
}

func TestAutoNameAIModeKeepsHeuristicWhenProviderFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "provider unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	p := newTestPlatform(t, map[string]any{
		"token":                   "secret",
		"auto_generate_name_mode": "ai",
		"name_api_key":            "provider-key",
		"name_base_url":           server.URL,
		"name_model":              "cheap-model",
	})
	sm := bindTestSessions(t, p)
	s, err := sm.NewSessionWithID("chat-api:owner", "conv1", "default")
	if err != nil {
		t.Fatalf("NewSessionWithID: %v", err)
	}

	query := "请帮我解释这段代码的结构"
	p.startAutoNameGeneration(newNameRunID(), s, sm, query)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		name := s.GetName()
		if name == autoNameFromQuery(query) {
			time.Sleep(200 * time.Millisecond)
			if s.GetName() == autoNameFromQuery(query) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("name = %q, want heuristic %q", s.GetName(), autoNameFromQuery(query))
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
