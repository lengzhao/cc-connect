package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type decisionTestPlatform struct {
	stubPlatformEngine
	handler func(string, string, string, string, string) (*Decision, error)
	cardMu  sync.Mutex
	cards   []Decision
	fail    bool
}

func (p *decisionTestPlatform) SetDecisionHandler(h func(string, string, string, string, string) (*Decision, error)) {
	p.handler = h
}
func (p *decisionTestPlatform) ResolveDecisionRecipient(_ context.Context, s string) (string, error) {
	return s, nil
}
func (p *decisionTestPlatform) SendDecision(_ context.Context, v *Decision) (string, error) {
	p.cardMu.Lock()
	defer p.cardMu.Unlock()
	if p.fail {
		return "", errors.New("transport failed")
	}
	p.cards = append(p.cards, *v)
	return "card-" + v.ID, nil
}
func (p *decisionTestPlatform) ReconstructReplyCtx(key string) (any, error) { return key, nil }
func decisionFixture(t *testing.T) (*Engine, *decisionTestPlatform, *Session, string) {
	t.Helper()
	p := &decisionTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("proj", &cujAgent{}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { e.cancel() })
	s := e.sessions.GetOrCreateActive("test:room:thread:root")
	m := &Message{SessionKey: "test:room:thread:root", Platform: "test", UserID: "alice", UserEmail: "alice@example.com", MessageID: "origin-message"}
	e.decisions.remember(p, m, s, e.sessions, m.SessionKey, "")
	return e, p, s, e.decisions.tokenFor(m.SessionKey, s.ID)
}
func decisionSpec() DecisionSpec {
	return DecisionSpec{Title: "Decision", Markdown: "**Review** [plan](https://example.com)", Options: []DecisionOption{{ID: "yes", Label: "Approve"}, {ID: "no", Label: "Revise"}}, AllowComment: true}
}
func TestDecisionScopeValidationAndCardIdempotency(t *testing.T) {
	e, p, s, token := decisionFixture(t)
	if _, err := e.decisions.create(context.Background(), "invalid", decisionSpec()); err == nil {
		t.Fatal("accepted unknown origin")
	}
	first, err := e.decisions.create(context.Background(), token, decisionSpec())
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.decisions.create(context.Background(), token, decisionSpec())
	if err != nil || first.ID != second.ID || len(p.cards) != 1 {
		t.Fatalf("duplicate card: %+v %v", second, err)
	}
	if first.Origin.SessionID != s.ID || first.DeliverySessionKey != "test:room:thread:root" {
		t.Fatalf("wrong route: %+v", first)
	}
	if _, err = p.handler(first.ID, "mallory", "yes", "", first.MessageID); err == nil {
		t.Fatal("wrong actor accepted")
	}
	if _, err = p.handler(first.ID, "alice", "unknown", "", first.MessageID); err == nil {
		t.Fatal("unknown option accepted")
	}
	if _, err = p.handler(first.ID, "alice", "yes", "", "other-card"); err == nil {
		t.Fatal("wrong card accepted")
	}
	if _, err = p.handler(first.ID, "alice", "yes", "looks good", first.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, err = p.handler(first.ID, "alice", "yes", "looks good", first.MessageID); err != nil {
		t.Fatalf("duplicate callback not idempotent: %v", err)
	}
	if _, err = p.handler(first.ID, "alice", "no", "", first.MessageID); err == nil {
		t.Fatal("changed accepted answer")
	}
}
func TestDecisionAnswerSurvivesRestartAndBusySession(t *testing.T) {
	e, p, s, token := decisionFixture(t)
	v, err := e.decisions.create(context.Background(), token, decisionSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.handler(v.ID, "alice", "yes", "approved", v.MessageID); err != nil {
		t.Fatal(err)
	}
	d := newDecisionService(e, strings.TrimSuffix(e.decisions.path, ".decisions"))
	e.decisions = d
	s.TryLock()
	d.drainOne()
	if d.items[v.ID].Status != "answered" {
		t.Fatal("busy session lost decision")
	}
	s.Unlock()
	d.drainOne()
	waitDecision(t, d, v.ID, "delivered")
	deadline := time.Now().Add(2 * time.Second)
	for s.Busy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	var count int
	for _, h := range s.GetHistory(0) {
		if h.Role == "user" && strings.Contains(h.Content, v.ID) {
			count++
			if h.UserID != "alice" {
				t.Fatal("origin identity replaced")
			}
		}
	}
	if count != 1 {
		t.Fatalf("decision delivered %d times", count)
	}
	d.drainOne()
	if len(p.getSent()) == 0 {
		t.Fatal("agent did not reply to original conversation")
	}
}
func waitDecision(t *testing.T, d *decisionService, id, status string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		got := d.items[id].Status
		d.mu.Unlock()
		if got == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("decision did not reach %s", status)
}
func TestDecisionCrossUserResetExpiryAndUncertainDelivery(t *testing.T) {
	e, p, _, token := decisionFixture(t)
	spec := decisionSpec()
	spec.Recipient = "bob"
	v, err := e.decisions.create(context.Background(), token, spec)
	if err != nil {
		t.Fatal(err)
	}
	if v.DeliverySessionKey != "" || v.RecipientID != "bob" || v.Origin.UserID != "alice" {
		t.Fatal("cross-user route changed origin")
	}
	if _, err = p.handler(v.ID, "alice", "yes", "", v.MessageID); err == nil {
		t.Fatal("creator approved for recipient")
	}
	if _, err = p.handler(v.ID, "bob", "yes", "", v.MessageID); err != nil {
		t.Fatal(err)
	}
	e.sessions.NewSession(v.Origin.SessionKey, "reset")
	e.decisions.drainOne()
	if e.decisions.items[v.ID].Status != "session_unavailable" {
		t.Fatal("answer routed to reset session")
	}
	if _, err = e.decisions.create(context.Background(), token, spec); err == nil {
		t.Fatal("stale tool token accepted")
	}
	e.decisions.items[v.ID].Status = "dispatching"
	if err = e.decisions.persistLocked(e.decisions.items[v.ID]); err != nil {
		t.Fatal(err)
	}
	restored := newDecisionService(e, strings.TrimSuffix(e.decisions.path, ".decisions"))
	if restored.items[v.ID].Status != "delivery_unknown" {
		t.Fatal("uncertain delivery would be replayed")
	}
	restored.items[v.ID].Status = "pending"
	restored.items[v.ID].ExpiresAt = time.Now().Add(-time.Minute)
	if _, err = restored.answer(v.ID, "bob", "yes", "", v.MessageID); err == nil {
		t.Fatal("expired card accepted")
	}
}
func TestDecisionPersistenceFailureDoesNotAcknowledge(t *testing.T) {
	e, p, _, token := decisionFixture(t)
	v, err := e.decisions.create(context.Background(), token, decisionSpec())
	if err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(t.TempDir(), "file")
	if err = os.WriteFile(block, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	e.decisions.path = filepath.Join(block, "store.json")
	if _, err = p.handler(v.ID, "alice", "yes", "", v.MessageID); err == nil {
		t.Fatal("acknowledged non-durable answer")
	}
	if e.decisions.items[v.ID].Status != "pending" {
		t.Fatal("failed persistence mutated request")
	}
}
func TestDecisionTransportFailureIsNotPending(t *testing.T) {
	e, p, _, token := decisionFixture(t)
	p.fail = true
	v, err := e.decisions.create(context.Background(), token, decisionSpec())
	if err == nil || v.Status != "send_unknown" {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestDecisionRestoresWorkspaceAndDoesNotLetBusySessionStarveOthers(t *testing.T) {
	e, p, first, token := decisionFixture(t)
	busy, err := e.decisions.create(context.Background(), token, decisionSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.handler(busy.ID, "alice", "yes", "", busy.MessageID); err != nil {
		t.Fatal(err)
	}
	first.TryLock()
	defer first.Unlock()
	workspace := t.TempDir()
	e.workspacePool = newWorkspacePool(DefaultWorkspaceIdleTimeout)
	ws := e.workspacePool.GetOrCreate(workspace)
	ws.agent = &cujAgent{}
	ws.sessions = NewSessionManager(filepath.Join(workspace, "sessions.json"))
	key := "test:other:thread:root"
	s := ws.sessions.GetOrCreateActive(key)
	msg := &Message{SessionKey: key, Platform: "test", UserID: "bob", MessageID: "second"}
	e.decisions.remember(p, msg, s, ws.sessions, workspace+":"+key, workspace)
	v, err := e.decisions.create(context.Background(), e.decisions.tokenFor(workspace+":"+key, s.ID), decisionSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.handler(v.ID, "bob", "yes", "workspace answer", v.MessageID); err != nil {
		t.Fatal(err)
	}
	e.decisions.drainOne()
	waitDecision(t, e.decisions, v.ID, "delivered")
	if len(first.GetHistory(0)) != 0 {
		t.Fatal("answer crossed into first session")
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.Busy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	found := false
	for _, h := range s.GetHistory(0) {
		if strings.Contains(h.Content, "workspace answer") {
			found = true
		}
	}
	if !found {
		t.Fatal("workspace session did not receive answer")
	}
	e.decisions.mu.Lock()
	status := e.decisions.items[busy.ID].Status
	e.decisions.mu.Unlock()
	if status != "answered" {
		t.Fatal("busy first session was consumed")
	}
}
