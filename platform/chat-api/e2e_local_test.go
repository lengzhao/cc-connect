package chatapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// e2eAgent drives Engine with a single EventResult reply per turn.
type e2eAgent struct {
	session *e2eAgentSession
}

func (a *e2eAgent) Name() string { return "e2e-stub" }
func (a *e2eAgent) StartSession(_ context.Context, _ string) (core.AgentSession, error) {
	return a.session, nil
}
func (a *e2eAgent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) { return nil, nil }
func (a *e2eAgent) Stop() error                                                     { return nil }

type e2eAgentSession struct {
	events chan core.Event
	reply  string
}

func newE2EAgentSession(reply string) *e2eAgentSession {
	return &e2eAgentSession{
		events: make(chan core.Event, 4),
		reply:  reply,
	}
}

func (s *e2eAgentSession) Send(_ string, _ []core.ImageAttachment, _ []core.FileAttachment) error {
	s.events <- core.Event{Type: core.EventResult, Content: s.reply, Done: true}
	return nil
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
	if accept != "" {
		req.Header.Set("Accept", accept)
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
	waitForHistory(t, base, token, convID, 1)

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
	if len(histResp.Data.Messages) != 1 {
		t.Fatalf("messages = %+v, want 1", histResp.Data.Messages)
	}
	if histResp.Data.Messages[0]["query"] != "用一句话介绍你自己" {
		t.Fatalf("query = %v", histResp.Data.Messages[0]["query"])
	}
	answer, _ := histResp.Data.Messages[0]["answer"].(string)
	if answer == "" {
		t.Fatalf("empty answer in history: %+v", histResp.Data.Messages[0])
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

	waitForHistory(t, base, token, convID, 2)

	resp, raw = e2eRequest(t, http.MethodGet, base+"/conversations/"+convID+"/messages?limit=20", token, "", nil, "")
	var hist2 struct {
		Data struct {
			Messages []map[string]any `json:"messages"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(raw), &hist2)
	if len(hist2.Data.Messages) != 2 {
		t.Fatalf("after continue messages = %d, want 2", len(hist2.Data.Messages))
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

	// 6. Delete
	resp, raw = e2eRequest(t, http.MethodDelete, base+"/conversations/"+convID, token, user, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", resp.StatusCode, raw)
	}

	resp, raw = e2eRequest(t, http.MethodGet, base+"/conversations?limit=20", token, user, nil, "")
	_ = json.Unmarshal([]byte(raw), &listResp)
	if len(listResp.Data.Conversations) != 0 {
		t.Fatalf("after delete conversations = %+v", listResp.Data.Conversations)
	}

	resp, raw = e2eRequest(t, http.MethodGet, base+"/conversations/"+convID+"/messages?limit=20", token, "", nil, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted messages status = %d, want 404", resp.StatusCode)
	}
}

func waitForHistory(t *testing.T, base, token, convID string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, raw := e2eRequest(t, http.MethodGet, base+"/conversations/"+convID+"/messages?limit=20", token, "", nil, "")
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
