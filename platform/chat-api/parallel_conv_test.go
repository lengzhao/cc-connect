package chatapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// blockingAgent holds the first Send until release is closed; later Sends finish immediately.
type blockingAgent struct {
	release      chan struct{}
	firstStarted chan struct{}
	startOnce    sync.Once
	sendCount    int
	mu           sync.Mutex
}

func (a *blockingAgent) Name() string { return "chat-api-block" }
func (a *blockingAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (a *blockingAgent) Stop() error { return nil }
func (a *blockingAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return &blockingAgentSession{agent: a, events: make(chan core.Event, 4)}, nil
}

type blockingAgentSession struct {
	agent  *blockingAgent
	events chan core.Event
}

func (s *blockingAgentSession) Send(prompt string, _ []core.ImageAttachment, _ []core.FileAttachment) error {
	s.agent.mu.Lock()
	s.agent.sendCount++
	n := s.agent.sendCount
	s.agent.mu.Unlock()

	if n == 1 {
		s.agent.startOnce.Do(func() { close(s.agent.firstStarted) })
		<-s.agent.release
	}
	s.events <- core.Event{Type: core.EventResult, Content: "ok:" + prompt, Done: true}
	return nil
}
func (s *blockingAgentSession) RespondPermission(string, core.PermissionResult) error { return nil }
func (s *blockingAgentSession) Events() <-chan core.Event                             { return s.events }
func (s *blockingAgentSession) CurrentSessionID() string                              { return "block-sess" }
func (s *blockingAgentSession) Alive() bool                                           { return true }
func (s *blockingAgentSession) Close() error                                          { return nil }

func TestE2E_NewConversationsProcessInParallel(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "sessions.json")
	plat, err := New(map[string]any{
		"listen_addr": "127.0.0.1:0",
		"token":       "e2e-token",
		"path":        "/v1/",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := plat.(*Platform)

	release := make(chan struct{})
	firstStarted := make(chan struct{})
	agent := &blockingAgent{release: release, firstStarted: firstStarted}
	engine := core.NewEngine("e2e", agent, []core.Platform{p}, sessionPath, core.LangChinese)
	if err := engine.Start(); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
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
	base := p.ResolvedBaseURL()

	// Conversation A: empty conversation_id, blocks inside agent.Send.
	errCh := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, base+"/chat-messages",
			strings.NewReader(`{"query":"first long task"}`))
		req.Header.Set("Authorization", "Bearer e2e-token")
		req.Header.Set("X-Chat-API-User", "user_parallel")
		req.Header.Set("X-Chat-API-Channel", testChannel)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
		errCh <- nil
	}()

	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first conversation did not reach agent.Send")
	}

	// Conversation B: another empty conversation_id while A is still busy.
	req2, _ := http.NewRequest(http.MethodPost, base+"/chat-messages",
		strings.NewReader(`{"query":"second independent"}`))
	req2.Header.Set("Authorization", "Bearer e2e-token")
	req2.Header.Set("X-Chat-API-User", "user_parallel")
	req2.Header.Set("X-Chat-API-Channel", testChannel)
	req2.Header.Set("Accept", "text/event-stream")
	req2.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	defer resp2.Body.Close()
	raw2, _ := io.ReadAll(resp2.Body)
	body2 := string(raw2)
	t.Logf("second SSE:\n%s", body2)

	if strings.Contains(body2, "message_queued") {
		t.Fatalf("new conversation_id got message_queued; expected parallel processing")
	}
	if !strings.Contains(body2, "event: message_end") {
		t.Fatalf("expected message_end on second conversation, got: %s", body2)
	}

	var convID string
	for _, block := range strings.Split(body2, "\n\n") {
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
