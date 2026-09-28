package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestInteractiveCardCrossSessionDoesNotResumeOrigin(t *testing.T) {
	e, p, s, token := decisionFixture(t)
	v, err := e.decisions.manage(context.Background(), interactiveCardRequest{Token: token, Action: "create", Spec: decisionSpec()})
	if err != nil {
		t.Fatal(err)
	}
	if v.ReturnMode != "none" || v.OwnerAutomon != "proj" {
		t.Fatal("missing ownership/return mode")
	}
	if _, err = p.handler(v.ID, "alice", "yes", "approved", v.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, err = p.handler(v.ID, "alice", "yes", "approved", v.MessageID); err != nil {
		t.Fatal(err)
	}
	e.decisions.drainOne()
	got, err := e.decisions.managedCard(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "answered" || got.Revision != 1 || len(got.Interactions) != 1 {
		t.Fatalf("lost/double interaction: %+v", got)
	}
	for _, h := range s.GetHistory(0) {
		if strings.Contains(h.Content, v.ID) {
			t.Fatal("notification woke session")
		}
	}
	stale := 0
	if _, err = e.decisions.manage(context.Background(), interactiveCardRequest{Token: token, Action: "update", CardID: v.ID, ExpectedRevision: &stale, Spec: decisionSpec()}); err == nil {
		t.Fatal("update based on pre-click state overwrote an interaction")
	}
	// A new authenticated conversation manages the card without rewriting origin.
	next := e.sessions.GetOrCreateActive("test:other")
	m := &Message{SessionKey: "test:other", Platform: "test", UserID: "alice", MessageID: "next"}
	e.decisions.remember(p, m, next, e.sessions, m.SessionKey, "")
	tok := e.decisions.tokenFor(m.SessionKey, next.ID)
	rev := got.Revision
	got, err = e.decisions.manage(context.Background(), interactiveCardRequest{Token: tok, Action: "update", CardID: v.ID, ExpectedRevision: &rev, Spec: decisionSpec()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Origin.SessionID != s.ID || got.Revision != rev+1 || got.ReturnMode != "none" {
		t.Fatal("origin/mode changed")
	}
	if _, err = e.decisions.manage(context.Background(), interactiveCardRequest{Token: tok, Action: "update", CardID: v.ID, ExpectedRevision: &rev, Spec: DecisionSpec{Title: "different", Markdown: "different"}}); err == nil {
		t.Fatal("stale update accepted")
	}
	if _, err = p.handler(v.ID, "alice", "no", "", v.MessageID, map[string]any{"_revision": 0}); err == nil {
		t.Fatal("stale click accepted")
	}
	rev = got.Revision
	if _, err = e.decisions.manage(context.Background(), interactiveCardRequest{Token: tok, Action: "close", CardID: v.ID, ExpectedRevision: &rev}); err != nil {
		t.Fatal(err)
	}
	e.decisions = newDecisionService(e, strings.TrimSuffix(e.decisions.path, ".decisions"))
	got, err = e.decisions.managedCard(v.ID)
	if err != nil || got.Status != "closed" || len(got.Interactions) != 1 {
		t.Fatal("restart lost state", err)
	}
	if _, err = e.decisions.answer(v.ID, "alice", "yes", "", v.MessageID); err == nil {
		t.Fatal("closed click accepted")
	}
}

func TestInteractiveCardRejectsUntrustedContextAndKeepsAskUserReturn(t *testing.T) {
	e, _, _, token := decisionFixture(t)
	v, err := e.decisions.create(context.Background(), token, decisionSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.decisions.manage(context.Background(), interactiveCardRequest{Token: "fake", Action: "get", CardID: v.ID}); err == nil {
		t.Fatal("unauthenticated read")
	}
	rev := v.Revision
	v, err = e.decisions.manage(context.Background(), interactiveCardRequest{Token: token, Action: "update", CardID: v.ID, ExpectedRevision: &rev, Spec: decisionSpec()})
	if err != nil || v.ReturnMode != "session" {
		t.Fatal("ask_user return mode changed", err)
	}
	other := NewEngine("other", &cujAgent{}, nil, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	defer other.cancel()
	if _, err = other.decisions.managedCard(v.ID); err == nil {
		t.Fatal("cross Automon read")
	}
}
