package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type contextRefreshTestAgent struct {
	resources []ContextResource
}

func (a *contextRefreshTestAgent) Name() string { return "context-test" }
func (a *contextRefreshTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	return nil, errors.New("not implemented")
}
func (a *contextRefreshTestAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a *contextRefreshTestAgent) Stop() error { return nil }
func (a *contextRefreshTestAgent) ContextResources() ([]ContextResource, error) {
	return append([]ContextResource(nil), a.resources...), nil
}

type contextRefreshTestSession struct {
	prompts []string
	fail    bool
}

func (s *contextRefreshTestSession) Send(prompt string, _ []ImageAttachment, _ []FileAttachment) error {
	s.prompts = append(s.prompts, prompt)
	if s.fail {
		return errors.New("send failed")
	}
	return nil
}
func (s *contextRefreshTestSession) RespondPermission(string, PermissionResult) error { return nil }
func (s *contextRefreshTestSession) Events() <-chan Event                             { return nil }
func (s *contextRefreshTestSession) CurrentSessionID() string                         { return "agent-session" }
func (s *contextRefreshTestSession) Alive() bool                                      { return true }
func (s *contextRefreshTestSession) Close() error                                     { return nil }

func TestSendWithContextRefreshBaselinesThenInjectsOneTurn(t *testing.T) {
	agent := &contextRefreshTestAgent{resources: []ContextResource{{
		Kind: "automon", Path: "/runtime/AUTOMON.md", Version: "v1", UpdatedAt: time.Unix(1, 0),
	}}}
	conversation := &Session{ID: "conv-1"}
	agentSession := &contextRefreshTestSession{}
	manager := NewSessionManager("")
	engine := &Engine{}

	if err := engine.sendWithContextRefresh(agent, agentSession, conversation, manager, "first", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := agentSession.prompts[0]; got != "first" {
		t.Fatalf("first prompt = %q, want no refresh notice", got)
	}

	agent.resources[0].Version = "v2"
	agent.resources[0].UpdatedAt = time.Unix(2, 0)
	if err := engine.sendWithContextRefresh(agent, agentSession, conversation, manager, "second", nil, nil); err != nil {
		t.Fatal(err)
	}
	second := agentSession.prompts[1]
	if !strings.Contains(second, "<automon_context_updates>") ||
		!strings.Contains(second, "automon updated: /runtime/AUTOMON.md") ||
		!strings.Contains(second, "<current_user_request>\nsecond") {
		t.Fatalf("second prompt missing refresh notice: %q", second)
	}

	if err := engine.sendWithContextRefresh(agent, agentSession, conversation, manager, "third", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := agentSession.prompts[2]; got != "third" {
		t.Fatalf("third prompt repeated refresh notice: %q", got)
	}
}

func TestSendWithContextRefreshDoesNotAdvanceOnSendFailure(t *testing.T) {
	agent := &contextRefreshTestAgent{resources: []ContextResource{{Kind: "memory", Path: "files/memory/a.md", Version: "v1"}}}
	conversation := &Session{ID: "conv-1"}
	agentSession := &contextRefreshTestSession{}
	manager := NewSessionManager("")
	engine := &Engine{}
	if err := engine.sendWithContextRefresh(agent, agentSession, conversation, manager, "baseline", nil, nil); err != nil {
		t.Fatal(err)
	}

	agent.resources[0].Version = "v2"
	agentSession.fail = true
	if err := engine.sendWithContextRefresh(agent, agentSession, conversation, manager, "failed", nil, nil); err == nil {
		t.Fatal("send unexpectedly succeeded")
	}
	agentSession.fail = false
	if err := engine.sendWithContextRefresh(agent, agentSession, conversation, manager, "retry", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(agentSession.prompts[len(agentSession.prompts)-1], "memory updated: files/memory/a.md") {
		t.Fatal("retry did not retain pending memory refresh")
	}
}

func TestContextRefreshDetectsDeletionWithEmptySnapshot(t *testing.T) {
	agent := &contextRefreshTestAgent{resources: []ContextResource{{Kind: "skill", Path: "/skills/a/SKILL.md", Version: "v1"}}}
	conversation := &Session{ID: "conv-1"}
	agentSession := &contextRefreshTestSession{}
	manager := NewSessionManager("")
	engine := &Engine{}
	if err := engine.sendWithContextRefresh(agent, agentSession, conversation, manager, "baseline", nil, nil); err != nil {
		t.Fatal(err)
	}
	agent.resources = nil
	if err := engine.sendWithContextRefresh(agent, agentSession, conversation, manager, "after delete", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(agentSession.prompts[1], "skill deleted: /skills/a/SKILL.md") {
		t.Fatalf("delete prompt = %q", agentSession.prompts[1])
	}
	if err := engine.sendWithContextRefresh(agent, agentSession, conversation, manager, "stable empty", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := agentSession.prompts[2]; got != "stable empty" {
		t.Fatalf("empty checkpoint did not persist: %q", got)
	}
}
