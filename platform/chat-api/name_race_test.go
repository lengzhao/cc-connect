package chatapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// slowAgent delays every Send so name-generation and chat can race on TryLock.
type slowAgent struct {
	mu    sync.Mutex
	sends []string
	delay time.Duration
}

func (a *slowAgent) Name() string { return "chat-api-slow" }
func (a *slowAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (a *slowAgent) Stop() error { return nil }
func (a *slowAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return &slowAgentSession{agent: a, events: make(chan core.Event, 4)}, nil
}

type slowAgentSession struct {
	agent  *slowAgent
	events chan core.Event
}

func (s *slowAgentSession) Send(prompt string, _ []core.ImageAttachment, _ []core.FileAttachment) error {
	s.agent.mu.Lock()
	s.agent.sends = append(s.agent.sends, prompt)
	s.agent.mu.Unlock()
	time.Sleep(s.agent.delay)
	s.events <- core.Event{Type: core.EventResult, Content: "named-or-answered", Done: true}
	return nil
}
func (s *slowAgentSession) RespondPermission(string, core.PermissionResult) error { return nil }
func (s *slowAgentSession) Events() <-chan core.Event                             { return s.events }
func (s *slowAgentSession) CurrentSessionID() string                              { return "slow-sess" }
func (s *slowAgentSession) Alive() bool                                           { return true }
func (s *slowAgentSession) Close() error                                          { return nil }

func TestE2E_AIAutoNameDoesNotQueueFirstChatMessage(t *testing.T) {
	plat, err := New(map[string]any{
		"listen_addr":             "127.0.0.1:0",
		"token":                   "e2e-token",
		"path":                    "/v1/",
		"auto_generate_name_mode": "ai",
		// No name_api_key: must not fall back to agent on the chat session.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := plat.(*Platform)

	agent := &slowAgent{delay: 50 * time.Millisecond}
	engine := core.NewEngine("e2e", agent, []core.Platform{p}, "", core.LangChinese)
	if err := engine.Start(); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Stop()
		_ = p.Stop()
	})

	deadline := time.Now().Add(2 * time.Second)
	for p.ResolvedBaseURL() == "" {
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// query >= 8 runes so shouldGenerateAIName is true
	body := `{"query":"请帮我分析这段代码的结构"}`
	req, _ := http.NewRequest(http.MethodPost, p.ResolvedBaseURL()+"/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer e2e-token")
	req.Header.Set("X-Chat-API-User", "user_name_race")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := string(raw)
	t.Logf("SSE:\n%s", out)

	if strings.Contains(out, "message_queued") {
		t.Fatalf("auto-name must not queue the first chat turn; got: %s", out)
	}
	if !strings.Contains(out, "event: message_end") {
		t.Fatalf("expected message_end, got: %s", out)
	}

	var convID string
	for _, block := range strings.Split(out, "\n\n") {
		if !strings.Contains(block, "event: message\n") {
			continue
		}
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var payload struct {
				ConversationID string `json:"conversation_id"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload) == nil {
				convID = payload.ConversationID
			}
		}
	}
	if !isOpaqueConversationID(convID) {
		t.Fatalf("conversation_id = %q", convID)
	}
}
