package core

import (
	"context"
	"testing"
	"time"
)

func TestDecisionUpdateKeepsCardAndFencesRevisions(t *testing.T) {
	e, p, s, token := decisionFixture(t)
	spec := decisionSpec()
	spec.Fields = []DecisionField{{ID: "branch", Label: "Branch", Type: "text", Required: true}}
	spec.Options = []DecisionOption{{ID: "check", Label: "Check", Intermediate: true}, {ID: "build", Label: "Build"}}
	v, err := e.decisions.create(context.Background(), token, spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.handler(v.ID, "alice", "check", "", v.MessageID, map[string]any{"branch": "develop"}); err != nil {
		t.Fatal(err)
	}
	e.decisions.drainOne()
	waitDecision(t, e.decisions, v.ID, "delivered")
	deadline := time.Now().Add(3 * time.Second)
	for s.Busy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	spec.RequestID = v.ID
	spec.ExpectedRevision = 0
	spec.Markdown = "Checked. Confirm or modify."
	next, err := e.decisions.create(context.Background(), token, spec)
	if err != nil {
		t.Fatal(err)
	}
	if next.MessageID != v.MessageID || next.Revision != 1 || len(p.cards) != 1 || len(p.updates) != 1 || next.Spec.Fields[0].Default != "develop" {
		t.Fatalf("wrong update %+v", next)
	}
	again, err := e.decisions.create(context.Background(), token, spec)
	if err != nil || again.Revision != 1 || len(p.updates) != 1 {
		t.Fatalf("not idempotent %v", err)
	}
	// A late completion from the prior revision cannot close the new form.
	e.decisions.sent("decision:"+v.ID, nil)
	if e.decisions.items[v.ID].Status != "pending" {
		t.Fatal("old completion closed new form")
	}
	if _, err = p.handler(v.ID, "alice", "build", "", v.MessageID, map[string]any{"branch": "develop"}); err == nil {
		t.Fatal("old revision accepted")
	}
	if _, err = p.handler(v.ID, "alice", "build", "", v.MessageID, map[string]any{"_revision": "1", "branch": "main"}); err != nil {
		t.Fatal(err)
	}
	spec.ExpectedRevision = 1
	if _, err = e.decisions.create(context.Background(), token, spec); err == nil {
		t.Fatal("final answer reopened")
	}
	e.decisions.drainOne()
	waitDecision(t, e.decisions, v.ID, "delivered")
	deadline = time.Now().Add(3 * time.Second)
	for s.Busy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	count := 0
	for _, h := range s.GetHistory(0) {
		if h.Role == "user" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("want two continuations, got %d", count)
	}
}
func TestDecisionUpdateFailureRetryAndScope(t *testing.T) {
	e, p, _, token := decisionFixture(t)
	spec := decisionSpec()
	spec.Options[0].Intermediate = true
	v, err := e.decisions.create(context.Background(), token, spec)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.handler(v.ID, "alice", "yes", "", v.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.decisions.setStatus(v.ID, "delivered", ""); err != nil {
		t.Fatal(err)
	}
	spec.RequestID = v.ID
	spec.Markdown = "Next"
	p.updateFail = true
	if _, err = e.decisions.create(context.Background(), token, spec); err == nil {
		t.Fatal("patch failure hidden")
	}
	restored := newDecisionService(e, e.sessions.storePath)
	if restored.items[v.ID].Status != "update_unknown" {
		t.Fatal("uncertain update not durable")
	}
	other := v.Origin
	other.UserID = "mallory"
	if _, err = e.decisions.update(context.Background(), other, spec); err == nil {
		t.Fatal("wrong sender updated card")
	}
	p.updateFail = false
	next, err := e.decisions.create(context.Background(), token, spec)
	if err != nil || next.Revision != 1 || next.Status != "pending" {
		t.Fatalf("retry failed %+v %v", next, err)
	}
	if len(p.cards) != 1 {
		t.Fatal("update sent a new message")
	}
}

func TestDecisionClickDuringPatchIsNotOverwritten(t *testing.T) {
	e, p, _, token := decisionFixture(t)
	spec := decisionSpec()
	spec.Options[0].Intermediate = true
	v, err := e.decisions.create(context.Background(), token, spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.handler(v.ID, "alice", "yes", "", v.MessageID); err != nil {
		t.Fatal(err)
	}
	_ = e.decisions.setStatus(v.ID, "delivered", "")
	spec.RequestID = v.ID
	spec.Options = append([]DecisionOption(nil), spec.Options...)
	spec.Options[0].Intermediate = false
	p.updateHook = func(next *Decision) {
		if _, err := p.handler(v.ID, "alice", "yes", "", v.MessageID, map[string]any{"_revision": "1"}); err != nil {
			t.Error(err)
		}
	}
	next, err := e.decisions.create(context.Background(), token, spec)
	if err != nil || next.Status != "answered" {
		t.Fatalf("early click lost: %+v %v", next, err)
	}
}
