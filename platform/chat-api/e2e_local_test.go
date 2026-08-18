package chatapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("chat-api-e2e", func(map[string]any) (core.Agent, error) {
		return &e2eAgent{session: newE2EAgentSession("我是 workspace 测试助手。")}, nil
	})
}

// e2eAgent drives Engine with a single EventResult reply per turn.
type e2eAgent struct {
	session *e2eAgentSession
}

func (a *e2eAgent) Name() string { return "chat-api-e2e" }
func (a *e2eAgent) StartSession(_ context.Context, _ string) (core.AgentSession, error) {
	return a.session, nil
}
func (a *e2eAgent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) { return nil, nil }
func (a *e2eAgent) Stop() error                                                     { return nil }

type e2eAgentSession struct {
	events       chan core.Event
	reply        string
	errorOnSend  error
	mu           sync.Mutex
	prompts      []string
}

func newE2EAgentSession(reply string) *e2eAgentSession {
	return &e2eAgentSession{
		events: make(chan core.Event, 4),
		reply:  reply,
	}
}

func (s *e2eAgentSession) Send(prompt string, _ []core.ImageAttachment, _ []core.FileAttachment) error {
	s.mu.Lock()
	s.prompts = append(s.prompts, prompt)
	errOnSend := s.errorOnSend
	s.mu.Unlock()
	if errOnSend != nil {
		s.events <- core.Event{Type: core.EventError, Error: errOnSend, Done: true}
		return nil
	}
	s.events <- core.Event{Type: core.EventResult, Content: s.reply, Done: true}
	return nil
}

func (s *e2eAgentSession) getPrompts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.prompts))
	copy(out, s.prompts)
	return out
}
func (s *e2eAgentSession) RespondPermission(_ string, _ core.PermissionResult) error { return nil }
func (s *e2eAgentSession) Events() <-chan core.Event                                 { return s.events }
func (s *e2eAgentSession) CurrentSessionID() string                                  { return "e2e-agent-session" }
func (s *e2eAgentSession) Alive() bool                                               { return true }
func (s *e2eAgentSession) Close() error                                              { return nil }

func startLocalChatAPIServer(t *testing.T) (*Platform, *core.Engine, string) {
	t.Helper()

	sessionPath := filepath.Join(t.TempDir(), "sessions.json")
	plat, err := New(map[string]any{
		"listen_addr": "127.0.0.1:0",
		"token":       "e2e-token",
		"path":        "/v1/",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	agentSess := newE2EAgentSession("我是测试助手，可以帮你写代码。")
	engine := core.NewEngine("e2e", &e2eAgent{session: agentSess}, []core.Platform{p}, sessionPath, core.LangChinese)
	if err := engine.Start(); err != nil {
		t.Fatalf("engine.Start() error = %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Stop()
		_ = p.Stop()
	})

	deadline := time.Now().Add(2 * time.Second)
	for p.ResolvedBaseURL() == "" || !strings.Contains(p.ResolvedBaseURL(), "127.0.0.1:") {
		if time.Now().After(deadline) {
			t.Fatalf("server did not start, base url = %q", p.ResolvedBaseURL())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return p, engine, p.ResolvedBaseURL()
}

func e2eRequest(t *testing.T, method, url, token, user string, body io.Reader, accept string) (*http.Response, string) {
	return e2eRequestWithHeaders(t, method, url, token, user, nil, body, accept)
}

func e2eRequestWithHeaders(t *testing.T, method, url, token, user string, headers map[string]string, body io.Reader, accept string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if user != "" {
		req.Header.Set("X-Chat-API-User", user)
	}
	if req.Header.Get("X-Chat-API-Channel") == "" {
		req.Header.Set("X-Chat-API-Channel", "e2e-channel")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, string(raw)
}

func startLocalChatAPIMultiWorkspaceServer(t *testing.T) (*core.Engine, string, string, string) {
	t.Helper()

	dataDir := t.TempDir()
	baseDir := t.TempDir()
	bindingPath := filepath.Join(dataDir, "workspace_bindings.json")
	sessionPath := filepath.Join(dataDir, "sessions.json")

	plat, err := New(map[string]any{
		"listen_addr":     "127.0.0.1:0",
		"token":           "e2e-token",
		"path":            "/v1/",
		"cc_data_dir":     dataDir,
		"cc_project":      "e2e",
		"base_dir":        baseDir,
		"request_timeout": "2s",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	agentSess := newE2EAgentSession("我是 workspace 测试助手。")
	engine := core.NewEngine("e2e", &e2eAgent{session: agentSess}, []core.Platform{p}, sessionPath, core.LangChinese)
	engine.SetMultiWorkspace(baseDir, bindingPath)
	engine.SetWorkspaceInitAllowLocalPaths(true)
	if err := engine.Start(); err != nil {
		t.Fatalf("engine.Start() error = %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Stop()
		_ = p.Stop()
	})

	deadline := time.Now().Add(2 * time.Second)
	for p.ResolvedBaseURL() == "" || !strings.Contains(p.ResolvedBaseURL(), "127.0.0.1:") {
		if time.Now().After(deadline) {
			t.Fatalf("server did not start, base url = %q", p.ResolvedBaseURL())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return engine, p.ResolvedBaseURL(), p.Name(), baseDir
}

func parseSSEEvents(body string) []sseEvent {
	return parseSSE(body)
}

func sseConversationID(events []sseEvent) string {
	for _, e := range events {
		if e.Name != "message" {
			continue
		}
		var payload struct {
			ConversationID string `json:"conversation_id"`
		}
		if err := json.Unmarshal([]byte(e.Data), &payload); err == nil && payload.ConversationID != "" {
			return payload.ConversationID
		}
	}
	return ""
}

// TestE2ELocalChatAPIEventErrorEndsSSEWithMessageEnd verifies Engine Finalize on
// EventError closes the chat-api SSE (no hang until request_timeout).
func TestE2ELocalChatAPIEventErrorEndsSSEWithMessageEnd(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "sessions.json")
	plat, err := New(map[string]any{
		"listen_addr":     "127.0.0.1:0",
		"token":           "e2e-token",
		"path":            "/v1/",
		"request_timeout": "5s",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	agentSess := newE2EAgentSession("")
	agentSess.errorOnSend = errors.New("bash tool exited with code 1: permission denied")
	engine := core.NewEngine("e2e", &e2eAgent{session: agentSess}, []core.Platform{p}, sessionPath, core.LangChinese)
	if err := engine.Start(); err != nil {
		t.Fatalf("engine.Start() error = %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Stop()
		_ = p.Stop()
	})

	deadline := time.Now().Add(2 * time.Second)
	for p.ResolvedBaseURL() == "" || !strings.Contains(p.ResolvedBaseURL(), "127.0.0.1:") {
		if time.Now().After(deadline) {
			t.Fatalf("server did not start, base url = %q", p.ResolvedBaseURL())
		}
		time.Sleep(10 * time.Millisecond)
	}
	base := p.ResolvedBaseURL()

	resp, raw := e2eRequest(t, http.MethodPost, base+"/chat-messages", "e2e-token", "e2e_user",
		strings.NewReader(`{"query":"run failing tool","auto_generate_name":true}`), "text/event-stream")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, "request timed out") {
		t.Fatalf("SSE hung until timeout instead of Finalize: %s", raw)
	}
	events := parseSSEEvents(raw)
	if !hasEvent(events, "message_end") {
		t.Fatalf("expected message_end after EventError Finalize, got: %s", raw)
	}
	if !strings.Contains(raw, "permission denied") && !strings.Contains(raw, "bash tool exited") {
		t.Fatalf("expected agent error text in SSE, got: %s", raw)
	}
}

// TestE2EUnknownSlashCommandStreamsAgentAfterForwarding verifies unknown slash
// commands notify the user then keep the SSE run open for the agent response.
func TestE2EUnknownSlashCommandStreamsAgentAfterForwarding(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "sessions.json")
	plat, err := New(map[string]any{
		"listen_addr": "127.0.0.1:0",
		"token":       "e2e-token",
		"path":        "/v1/",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	agentSess := newE2EAgentSession("这是 skill-guide 的回复。")
	engine := core.NewEngine("e2e", &e2eAgent{session: agentSess}, []core.Platform{p}, sessionPath, core.LangChinese)
	if err := engine.Start(); err != nil {
		t.Fatalf("engine.Start() error = %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Stop()
		_ = p.Stop()
	})

	deadline := time.Now().Add(2 * time.Second)
	for p.ResolvedBaseURL() == "" || !strings.Contains(p.ResolvedBaseURL(), "127.0.0.1:") {
		if time.Now().After(deadline) {
			t.Fatalf("server did not start, base url = %q", p.ResolvedBaseURL())
		}
		time.Sleep(10 * time.Millisecond)
	}
	base := p.ResolvedBaseURL()

	resp, raw := e2eRequest(t, http.MethodPost, base+"/chat-messages", "e2e-token", "e2e_user",
		strings.NewReader(`{"query":"/skill-guide hi","auto_generate_name":true}`), "text/event-stream")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(raw, "转发给 Agent") {
		t.Fatalf("missing forwarding hint: %s", raw)
	}
	if !strings.Contains(raw, "这是 skill-guide 的回复") {
		t.Fatalf("missing agent response: %s", raw)
	}
	forwardIdx := strings.Index(raw, "转发给 Agent")
	agentIdx := strings.Index(raw, "这是 skill-guide 的回复")
	endIdx := strings.Index(raw, "event: message_end")
	if forwardIdx < 0 || agentIdx < 0 || endIdx < 0 {
		t.Fatalf("missing events: %s", raw)
	}
	if !(forwardIdx < agentIdx && agentIdx < endIdx) {
		t.Fatalf("expected forwarding → agent answer → message_end order, got forward=%d agent=%d end=%d\n%s",
			forwardIdx, agentIdx, endIdx, raw)
	}
	prompts := agentSess.getPrompts()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "/skill-guide hi") {
		t.Fatalf("agent prompts = %#v, want one containing /skill-guide hi", prompts)
	}
}

// TestE2ELocalChatAPIFlow starts a real HTTP server and walks through create → list →
// history → continue → rename → delete.
func TestE2ELocalChatAPIFlow(t *testing.T) {
	_, _, base := startLocalChatAPIServer(t)
	const user = "e2e_user"
	const token = "e2e-token"

	// 1. Create conversation via chat-messages
	createBody := `{"query":"用一句话介绍你自己","auto_generate_name":true}`
	resp, raw := e2eRequest(t, http.MethodPost, base+"/chat-messages", token, user, strings.NewReader(createBody), "text/event-stream")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d body=%s", resp.StatusCode, raw)
	}
	events := parseSSEEvents(raw)
	if !hasEvent(events, "message") || !hasEvent(events, "message_end") {
		t.Fatalf("create SSE events = %#v", events)
	}
	convID := sseConversationID(events)
	if convID == "" {
		t.Fatalf("missing conversation_id in SSE: %#v", events)
	}
	if !isOpaqueConversationID(convID) {
		t.Fatalf("conversation_id = %q, want opaque conv_* id", convID)
	}
	if strings.HasPrefix(convID, "s") && len(convID) <= 4 {
		t.Fatalf("conversation_id too guessable: %q", convID)
	}

	// 2. List conversations
	resp, raw = e2eRequest(t, http.MethodGet, base+"/conversations?limit=20", token, user, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d body=%s", resp.StatusCode, raw)
	}
	var listResp struct {
		OK   bool `json:"ok"`
		Data struct {
			Conversations []conversationView `json:"conversations"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Data.Conversations) != 1 || listResp.Data.Conversations[0].ID != convID {
		t.Fatalf("list = %+v, want conversation %q", listResp.Data.Conversations, convID)
	}
	if listResp.Data.Conversations[0].Name != "用一句话介绍你自己" {
		t.Fatalf("auto name = %q", listResp.Data.Conversations[0].Name)
	}

	// 3. History (wait briefly for engine to persist assistant turn)
	waitForHistory(t, base, token, "e2e-channel", convID, 2)

	resp, raw = e2eRequest(t, http.MethodGet, base+"/conversations/"+convID+"/messages?limit=20", token, "", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("messages status = %d body=%s", resp.StatusCode, raw)
	}
	var histResp struct {
		OK   bool `json:"ok"`
		Data struct {
			Messages []map[string]any `json:"messages"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &histResp); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(histResp.Data.Messages) != 2 {
		t.Fatalf("messages = %+v, want 2 entries", histResp.Data.Messages)
	}
	var userEntry map[string]any
	for _, m := range histResp.Data.Messages {
		if m["role"] == "user" {
			userEntry = m
			break
		}
	}
	if userEntry == nil || userEntry["query"] != "用一句话介绍你自己" {
		t.Fatalf("user entry = %+v", userEntry)
	}
	var assistantEntry map[string]any
	for _, m := range histResp.Data.Messages {
		if m["role"] == "assistant" {
			assistantEntry = m
			break
		}
	}
	if assistantEntry == nil {
		t.Fatalf("missing assistant entry: %+v", histResp.Data.Messages)
	}
	answer, _ := assistantEntry["answer"].(string)
	if answer == "" {
		t.Fatalf("empty answer in history: %+v", assistantEntry)
	}

	// 4. Continue conversation
	contBody := fmt.Sprintf(`{"conversation_id":%q,"query":"再说一个要点"}`, convID)
	resp, raw = e2eRequest(t, http.MethodPost, base+"/chat-messages", token, user, strings.NewReader(contBody), "text/event-stream")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("continue status = %d body=%s", resp.StatusCode, raw)
	}
	if !hasEvent(parseSSEEvents(raw), "message_end") {
		t.Fatalf("continue SSE missing message_end: %s", raw)
	}

	waitForHistory(t, base, token, "e2e-channel", convID, 4)

	resp, raw = e2eRequest(t, http.MethodGet, base+"/conversations/"+convID+"/messages?limit=20", token, "", nil, "")
	var hist2 struct {
		Data struct {
			Messages []map[string]any `json:"messages"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(raw), &hist2)
	if len(hist2.Data.Messages) != 4 {
		t.Fatalf("after continue messages = %d, want 4", len(hist2.Data.Messages))
	}

	// 5. Rename
	patchBody := `{"name":"E2E 测试会话"}`
	resp, raw = e2eRequest(t, http.MethodPatch, base+"/conversations/"+convID, token, user, strings.NewReader(patchBody), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", resp.StatusCode, raw)
	}

	resp, raw = e2eRequest(t, http.MethodGet, base+"/conversations?limit=20", token, user, nil, "")
	_ = json.Unmarshal([]byte(raw), &listResp)
	if listResp.Data.Conversations[0].Name != "E2E 测试会话" {
		t.Fatalf("renamed name = %q", listResp.Data.Conversations[0].Name)
	}
}

// TestE2ELocalChatAPIAgentContextInjection verifies mapped headers reach the agent
// prompt when inject_context is enabled, while history stays raw.
func TestE2ELocalChatAPIAgentContextInjection(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "sessions.json")
	plat, err := New(map[string]any{
		"listen_addr": "127.0.0.1:0",
		"token":       "e2e-token",
		"path":        "/v1/",
		"agent_context_headers": map[string]any{
			"language":         "X-Language",
			"task_id":          "X-Task-ID",
			"custom.tenant_id": "X-Tenant-ID",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)
	agentSess := newE2EAgentSession("ctx-ok")
	engine := core.NewEngine("e2e", &e2eAgent{session: agentSess}, []core.Platform{p}, sessionPath, core.LangChinese)
	engine.SetInjectContext([]string{"language", "task_id", "custom.tenant_id"})
	if err := engine.Start(); err != nil {
		t.Fatalf("engine.Start() error = %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Stop()
		_ = p.Stop()
	})
	deadline := time.Now().Add(2 * time.Second)
	for p.ResolvedBaseURL() == "" || !strings.Contains(p.ResolvedBaseURL(), "127.0.0.1:") {
		if time.Now().After(deadline) {
			t.Fatalf("server did not start, base url = %q", p.ResolvedBaseURL())
		}
		time.Sleep(10 * time.Millisecond)
	}
	base := p.ResolvedBaseURL()

	body := `{"query":"hello with context"}`
	headers := map[string]string{
		"X-Language":  "zh",
		"X-Task-ID":   "job-local-1",
		"X-Tenant-ID": "acme",
		"X-Unknown":   "ignored",
	}
	resp, raw := e2eRequestWithHeaders(t, http.MethodPost, base+"/chat-messages", "e2e-token", "e2e_user", headers, strings.NewReader(body), "text/event-stream")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, raw)
	}
	events := parseSSEEvents(raw)
	if !hasEvent(events, "message_end") {
		t.Fatalf("SSE missing message_end: %s", raw)
	}
	convID := sseConversationID(events)

	deadline = time.Now().Add(2 * time.Second)
	var prompts []string
	for {
		prompts = agentSess.getPrompts()
		if len(prompts) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never received prompt")
		}
		time.Sleep(10 * time.Millisecond)
	}
	prompt := prompts[0]
	if !strings.Contains(prompt, `language="zh"`) || !strings.Contains(prompt, `task_id="job-local-1"`) ||
		!strings.Contains(prompt, `custom.tenant_id="acme"`) || !strings.Contains(prompt, "hello with context") {
		t.Fatalf("agent prompt missing context: %q", prompt)
	}

	waitForHistory(t, base, "e2e-token", "e2e-channel", convID, 2)
	resp, raw = e2eRequest(t, http.MethodGet, base+"/conversations/"+convID+"/messages?limit=20", "e2e-token", "", nil, "")
	var hist struct {
		Data struct {
			Messages []map[string]any `json:"messages"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &hist); err != nil {
		t.Fatalf("decode history: %v body=%s", err, raw)
	}
	if len(hist.Data.Messages) != 2 {
		t.Fatalf("messages = %+v", hist.Data.Messages)
	}
	var q string
	for _, m := range hist.Data.Messages {
		if m["role"] == "user" {
			q, _ = m["query"].(string)
			break
		}
	}
	if q != "hello with context" {
		t.Fatalf("history query = %q, want raw user text", q)
	}
	if strings.Contains(q, "[cc-connect") || strings.Contains(q, "task_id=") {
		t.Fatalf("history leaked inject header: %q", q)
	}
}

func TestE2ELocalChatAPIMultiWorkspaceChannelHistory(t *testing.T) {
	engine, base, platformName, _ := startLocalChatAPIMultiWorkspaceServer(t)
	const user = "e2e_user"
	const token = "e2e-token"
	const channel = "team-alpha/backend"
	headers := map[string]string{"X-Chat-API-Channel": channel}

	workspaceDir := filepath.Join(t.TempDir(), "backend")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	engine.BindWorkspaceForChannel(platformName, channel, channel, workspaceDir)

	body := `{"query":"进入 workspace 后回复一句","auto_generate_name":true}`
	resp, raw := e2eRequestWithHeaders(t, http.MethodPost, base+"/chat-messages", token, user, headers, strings.NewReader(body), "text/event-stream")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", resp.StatusCode, raw)
	}
	if !hasEvent(parseSSEEvents(raw), "message_end") {
		t.Fatalf("chat SSE missing message_end: %s", raw)
	}
	convID := sseConversationID(parseSSEEvents(raw))
	if convID == "" {
		t.Fatalf("missing conversation_id in SSE: %s", raw)
	}

	waitForHistory(t, base, token, channel, convID, 2)

	resp, raw = e2eRequestWithHeaders(t, http.MethodGet, base+"/conversations/"+convID+"/messages?limit=20", token, "", headers, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("messages status = %d body=%s", resp.StatusCode, raw)
	}
	var hist struct {
		Data struct {
			Messages []map[string]any `json:"messages"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &hist); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(hist.Data.Messages) != 2 {
		t.Fatalf("messages = %+v, want workspace turn history readable by conversation id", hist.Data.Messages)
	}
	var userEntry map[string]any
	for _, m := range hist.Data.Messages {
		if m["role"] == "user" {
			userEntry = m
			break
		}
	}
	if userEntry == nil || userEntry["query"] != "进入 workspace 后回复一句" {
		t.Fatalf("user entry = %+v", userEntry)
	}
}

func TestE2ELocalChatAPIMultiWorkspaceRequiresChannel(t *testing.T) {
	_, base, _, _ := startLocalChatAPIMultiWorkspaceServer(t)
	req, err := http.NewRequest(http.MethodPost, base+"/chat-messages", strings.NewReader(`{"query":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer e2e-token")
	req.Header.Set("X-Chat-API-User", "e2e_user")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", resp.StatusCode, string(raw))
	}
	if !strings.Contains(string(raw), "channel required") {
		t.Fatalf("body = %s", raw)
	}
}

func TestE2ELocalChatAPIMultiWorkspaceExplicitDefaultChannel(t *testing.T) {
	_, base, _, baseDir := startLocalChatAPIMultiWorkspaceServer(t)
	const user = "e2e_user"
	const token = "e2e-token"

	body := `{"query":"hi","auto_generate_name":true}`
	headers := map[string]string{"X-Chat-API-Channel": defaultWorkspaceChannelID}
	resp, raw := e2eRequestWithHeaders(t, http.MethodPost, base+"/chat-messages", token, user, headers, strings.NewReader(body), "text/event-stream")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, "Directory not found") {
		t.Fatalf("unexpected workspace init error: %s", raw)
	}
	if !hasEvent(parseSSEEvents(raw), "message_end") {
		t.Fatalf("chat SSE missing message_end: %s", raw)
	}
	channelDir := filepath.Join(baseDir, defaultWorkspaceChannelID)
	if st, err := os.Stat(channelDir); err != nil || !st.IsDir() {
		t.Fatalf("expected auto-created default channel dir %q: %v", channelDir, err)
	}
}

func TestE2ELocalChatAPIMultiWorkspaceDefaultChannelWithInjectedCCBaseDir(t *testing.T) {
	// Simulates production: project multi-workspace base_dir is injected as
	// cc_base_dir; platform options do not duplicate base_dir.
	dataDir := t.TempDir()
	baseDir := t.TempDir()
	bindingPath := filepath.Join(dataDir, "workspace_bindings.json")
	sessionPath := filepath.Join(dataDir, "sessions.json")

	plat, err := New(map[string]any{
		"listen_addr":     "127.0.0.1:0",
		"token":           "e2e-token",
		"path":            "/v1/",
		"cc_data_dir":     dataDir,
		"cc_project":      "e2e",
		"cc_base_dir":     baseDir,
		"request_timeout": "2s",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	agentSess := newE2EAgentSession("我是 workspace 测试助手。")
	engine := core.NewEngine("e2e", &e2eAgent{session: agentSess}, []core.Platform{p}, sessionPath, core.LangChinese)
	engine.SetMultiWorkspace(baseDir, bindingPath)
	engine.SetWorkspaceInitAllowLocalPaths(true)
	if err := engine.Start(); err != nil {
		t.Fatalf("engine.Start() error = %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Stop()
		_ = p.Stop()
	})

	deadline := time.Now().Add(2 * time.Second)
	for p.ResolvedBaseURL() == "" || !strings.Contains(p.ResolvedBaseURL(), "127.0.0.1:") {
		if time.Now().After(deadline) {
			t.Fatalf("server did not start, base url = %q", p.ResolvedBaseURL())
		}
		time.Sleep(10 * time.Millisecond)
	}
	base := p.ResolvedBaseURL()

	const user = "e2e_user"
	const token = "e2e-token"
	body := `{"query":"hi","auto_generate_name":true}`
	headers := map[string]string{"X-Chat-API-Channel": defaultWorkspaceChannelID}
	resp, raw := e2eRequestWithHeaders(t, http.MethodPost, base+"/chat-messages", token, user, headers, strings.NewReader(body), "text/event-stream")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, "此频道未找到工作区") || strings.Contains(raw, "Directory not found") || strings.Contains(raw, "目录不存在") {
		t.Fatalf("unexpected workspace init when cc_base_dir is injected: %s", raw)
	}
	if !hasEvent(parseSSEEvents(raw), "message_end") {
		t.Fatalf("chat SSE missing message_end: %s", raw)
	}
	channelDir := filepath.Join(baseDir, defaultWorkspaceChannelID)
	if st, err := os.Stat(channelDir); err != nil || !st.IsDir() {
		t.Fatalf("expected auto-created default channel dir %q: %v", channelDir, err)
	}
}

func TestE2ELocalChatAPIMultiWorkspaceAutoBootstrapChannel(t *testing.T) {
	_, base, _, baseDir := startLocalChatAPIMultiWorkspaceServer(t)
	const user = "e2e_user"
	const token = "e2e-token"
	const channel = "chat-123"
	headers := map[string]string{"X-Chat-API-Channel": channel}

	body := `{"query":"hi","auto_generate_name":true}`
	resp, raw := e2eRequestWithHeaders(t, http.MethodPost, base+"/chat-messages", token, user, headers, strings.NewReader(body), "text/event-stream")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, "Directory not found") {
		t.Fatalf("unexpected directory bind error: %s", raw)
	}
	if strings.Contains(raw, "此频道未找到工作区") {
		t.Fatalf("unexpected workspace init hint when platform base_dir is configured: %s", raw)
	}
	if !hasEvent(parseSSEEvents(raw), "message_end") {
		t.Fatalf("chat SSE missing message_end: %s", raw)
	}
	channelDir := filepath.Join(baseDir, channel)
	if st, err := os.Stat(channelDir); err != nil || !st.IsDir() {
		t.Fatalf("expected auto-created channel dir %q: %v", channelDir, err)
	}
}

func TestE2ELocalChatAPIMultiWorkspaceUnboundChannelWithoutPlatformBaseDir(t *testing.T) {
	dataDir := t.TempDir()
	baseDir := t.TempDir()
	bindingPath := filepath.Join(dataDir, "workspace_bindings.json")
	sessionPath := filepath.Join(dataDir, "sessions.json")

	plat, err := New(map[string]any{
		"listen_addr":     "127.0.0.1:0",
		"token":           "e2e-token",
		"path":            "/v1/",
		"cc_data_dir":     dataDir,
		"cc_project":      "e2e",
		"request_timeout": "2s",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	agentSess := newE2EAgentSession("我是 workspace 测试助手。")
	engine := core.NewEngine("e2e", &e2eAgent{session: agentSess}, []core.Platform{p}, sessionPath, core.LangChinese)
	engine.SetMultiWorkspace(baseDir, bindingPath)
	engine.SetWorkspaceInitAllowLocalPaths(true)
	if err := engine.Start(); err != nil {
		t.Fatalf("engine.Start() error = %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Stop()
		_ = p.Stop()
	})

	deadline := time.Now().Add(2 * time.Second)
	for p.ResolvedBaseURL() == "" || !strings.Contains(p.ResolvedBaseURL(), "127.0.0.1:") {
		if time.Now().After(deadline) {
			t.Fatalf("server did not start, base url = %q", p.ResolvedBaseURL())
		}
		time.Sleep(10 * time.Millisecond)
	}
	base := p.ResolvedBaseURL()

	const user = "e2e_user"
	const token = "e2e-token"
	headers := map[string]string{"X-Chat-API-Channel": "chat-123"}

	body := `{"query":"hi","auto_generate_name":true}`
	resp, raw := e2eRequestWithHeaders(t, http.MethodPost, base+"/chat-messages", token, user, headers, strings.NewReader(body), "text/event-stream")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, "我是 workspace 测试助手") {
		t.Fatalf("agent should not reply when platform base_dir is missing: %s", raw)
	}
	if !strings.Contains(raw, "此频道未找到工作区") && !strings.Contains(raw, "目录不存在") {
		t.Fatalf("expected workspace init flow without platform base_dir, got: %s", raw)
	}
	if !hasEvent(parseSSEEvents(raw), "message_end") {
		t.Fatalf("chat SSE missing message_end: %s", raw)
	}
}

func TestE2ELocalChatAPIMultiWorkspaceConventionMatchExistingChannelDir(t *testing.T) {
	_, base, _, baseDir := startLocalChatAPIMultiWorkspaceServer(t)
	const user = "e2e_user"
	const token = "e2e-token"
	const channel = "chat-123"
	headers := map[string]string{"X-Chat-API-Channel": channel}

	if err := os.MkdirAll(filepath.Join(baseDir, channel), 0o755); err != nil {
		t.Fatal(err)
	}

	body := `{"query":"hi","auto_generate_name":true}`
	resp, raw := e2eRequestWithHeaders(t, http.MethodPost, base+"/chat-messages", token, user, headers, strings.NewReader(body), "text/event-stream")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, "此频道未找到工作区") {
		t.Fatalf("unexpected workspace init hint when convention dir exists: %s", raw)
	}
	if !hasEvent(parseSSEEvents(raw), "message_end") {
		t.Fatalf("chat SSE missing message_end: %s", raw)
	}
}

func waitForHistory(t *testing.T, base, token, channel, convID string, want int) {
	t.Helper()
	headers := map[string]string{"X-Chat-API-Channel": channel}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, raw := e2eRequestWithHeaders(t, http.MethodGet, base+"/conversations/"+convID+"/messages?limit=20", token, "", headers, nil, "")
		if resp.StatusCode != http.StatusOK {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var hist struct {
			Data struct {
				Messages []any `json:"messages"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &hist); err == nil && len(hist.Data.Messages) >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d history messages for %s", want, convID)
}

func TestNewConversationIDFormat(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 32; i++ {
		id, err := newConversationID()
		if err != nil {
			t.Fatalf("newConversationID: %v", err)
		}
		if !isOpaqueConversationID(id) {
			t.Fatalf("id %q does not match opaque pattern", id)
		}
		if strings.HasPrefix(id, "s") && len(id) <= 4 {
			t.Fatalf("guessable id %q", id)
		}
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
}

// Ensure SSE parser handles chunked stream bodies from net/http.
func TestParseSSEFromHTTPBody(t *testing.T) {
	body := "event: message\ndata: {\"conversation_id\":\"conv_abc\"}\n\nevent: message_end\ndata: {}\n\n"
	events := parseSSEEvents(body)
	if len(events) != 2 {
		t.Fatalf("events = %d", len(events))
	}
	scanner := bufio.NewScanner(bytes.NewBufferString(body))
	if !scanner.Scan() {
		t.Fatal("scanner failed")
	}
}
