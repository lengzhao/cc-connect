package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type receiptTestPlatform struct {
	decisionTestPlatform
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (p *receiptTestPlatform) RecordDecisionReceipt(ctx context.Context, v *Decision) error {
	p.calls.Add(1)
	close(p.started)
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestDecisionReceiptPrecedesContinuationAndAllButtonsCanUpdate(t *testing.T) {
	p := &receiptTestPlatform{decisionTestPlatform: decisionTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}, started: make(chan struct{}), release: make(chan struct{})}
	path := filepath.Join(t.TempDir(), "sessions.json")
	e := NewEngine("p", &cujAgent{}, []Platform{p}, path, LangEnglish)
	defer e.Stop()
	s := e.sessions.GetOrCreateActive("test:room")
	m := &Message{SessionKey: "test:room", UserID: "alice", MessageID: "origin"}
	e.decisions.remember(p, m, s, e.sessions, m.SessionKey, "")
	token := e.decisions.tokenFor(m.SessionKey, s.ID)
	spec := decisionSpec()
	v, err := e.decisions.create(context.Background(), token, spec)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := p.handler(v.ID, "alice", "yes", "", v.MessageID); done <- err }()
	<-p.started
	data, err := os.ReadFile(filepath.Join(path+".decisions", v.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Decision
	_ = json.Unmarshal(data, &saved)
	if saved.Status != "recorded" || saved.OptionID != "yes" {
		t.Fatal("receipt started before durable save")
	}
	e.decisions.drainOne()
	if len(s.GetHistory(0)) != 0 {
		t.Fatal("Agent ran before receipt finished")
	}
	spec.RequestID = v.ID
	if _, err = e.decisions.create(context.Background(), token, spec); err == nil {
		t.Fatal("updated before receipt")
	}
	close(p.release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if _, err = p.handler(v.ID, "alice", "yes", "", v.MessageID); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("duplicate click wrote receipt again")
	}
	e.decisions.drainOne()
	waitDecision(t, e.decisions, v.ID, "delivered")
	deadline := time.Now().Add(time.Second)
	for s.Busy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// A previously ordinary/final button is just an interaction now.
	spec.Options = nil
	spec.Fields = nil
	spec.Markdown = "Agent-owned result"
	next, err := e.decisions.create(context.Background(), token, spec)
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != "displayed" || len(p.cards) != 1 {
		t.Fatalf("result should update same card %+v", next)
	}
	spec.ExpectedRevision = next.Revision
	spec.Options = []DecisionOption{{ID: "again", Label: "Try another"}}
	if _, err = e.decisions.create(context.Background(), token, spec); err != nil {
		t.Fatal("Agent cannot add next actions", err)
	}
}
