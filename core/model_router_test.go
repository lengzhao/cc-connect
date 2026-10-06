package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

type routeTestAgent struct {
	stubAgent
	model, resumed string
}

func (a *routeTestAgent) StartSessionWithModel(_ context.Context, id, model string) (AgentSession, error) {
	a.model = model
	a.resumed = id
	return &stubAgentSession{}, nil
}
func TestModelRouterScopeIsolationFallbackAndManualPin(t *testing.T) {
	agent := &routeTestAgent{}
	e := NewEngine("route", agent, nil, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	defer e.Stop()
	calls := 0
	e.SetModelRouter(func(_ context.Context, r ModelRouteRequest) (string, error) {
		calls++
		if r.Content == "error" {
			return "", errors.New("failed")
		}
		return r.Content, nil
	}, false)
	msg := &Message{SessionKey: "thread1", Content: "high"}
	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	if got := e.routeTurnModel(msg, s, e.sessions, agent, msg.SessionKey); got != "high" {
		t.Fatal(got)
	}
	msg.Content = "low"
	if got := e.routeTurnModel(msg, s, e.sessions, agent, msg.SessionKey); got != "high" || calls != 1 {
		t.Fatal(got, calls)
	}
	reloaded := NewSessionManager(e.sessions.StorePath()).GetActive(msg.SessionKey)
	if reloaded.AutomaticModel == nil || *reloaded.AutomaticModel != "high" {
		t.Fatal("session decision not persisted")
	}
	msg.SessionKey = "thread2"
	s2 := e.sessions.GetOrCreateActive(msg.SessionKey)
	if got := e.routeTurnModel(msg, s2, e.sessions, agent, msg.SessionKey); got != "low" {
		t.Fatal(got)
	}
	e.modelRoutePerTurn = true
	msg.Content = "error"
	if got := e.routeTurnModel(msg, s2, e.sessions, agent, msg.SessionKey); got != "" {
		t.Fatal("stale model on failure", got)
	}
	e.persistWorkspaceModelOverride(msg.SessionKey, msg.SessionKey, agent, "manual")
	n := calls
	msg.Content = "high"
	if got := e.routeTurnModel(msg, s2, e.sessions, agent, msg.SessionKey); got != "" || calls != n {
		t.Fatal("manual pin ignored")
	}
	if _, err := startRoutedSession(context.Background(), agent, "history-id", "high"); err != nil || agent.resumed != "history-id" || agent.model != "high" {
		t.Fatal("resume/override lost")
	}
}
