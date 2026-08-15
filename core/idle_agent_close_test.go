package core

import (
	"testing"
	"time"
)

func TestCloseIdleAgentSessionsResult_Fields(t *testing.T) {
	r := CloseIdleAgentSessionsResult{
		Closed:             1,
		Skipped:            2,
		ClosedSessionKeys:  []string{"a"},
		SkippedSessionKeys: []string{"b", "c"},
	}
	if r.Closed != 1 || r.Skipped != 2 {
		t.Fatalf("unexpected counts: %+v", r)
	}
	if len(r.ClosedSessionKeys) != 1 || len(r.SkippedSessionKeys) != 2 {
		t.Fatalf("unexpected keys: %+v", r)
	}
}

func TestCloseIdleAgentSessions_ClosesIdlePreservesBusy(t *testing.T) {
	e := newTestEngine()
	idleKey := "test:idle"
	busyKey := "test:busy"

	idleSess := newControllableSession("idle-agent")
	busySess := newControllableSession("busy-agent")

	idleState := &interactiveState{agentSession: idleSess, eventsNeedResync: false}
	busyState := &interactiveState{agentSession: busySess, eventsNeedResync: false}

	e.interactiveMu.Lock()
	e.interactiveStates[idleKey] = idleState
	e.interactiveStates[busyKey] = busyState
	e.interactiveMu.Unlock()

	busySession := e.sessions.GetOrCreateActive(busyKey)
	if !busySession.TryLock() {
		t.Fatal("expected TryLock to succeed for busy session")
	}
	t.Cleanup(func() { busySession.Unlock() })

	// Idle session exists but is not Busy.
	_ = e.sessions.GetOrCreateActive(idleKey)

	result := e.CloseIdleAgentSessions()

	if result.Closed != 1 || result.Skipped != 1 {
		t.Fatalf("result counts = closed=%d skipped=%d, want closed=1 skipped=1; keys=%+v",
			result.Closed, result.Skipped, result)
	}
	if !containsString(result.ClosedSessionKeys, idleKey) {
		t.Fatalf("ClosedSessionKeys = %v, want %q", result.ClosedSessionKeys, idleKey)
	}
	if !containsString(result.SkippedSessionKeys, busyKey) {
		t.Fatalf("SkippedSessionKeys = %v, want %q", result.SkippedSessionKeys, busyKey)
	}

	select {
	case <-idleSess.closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("idle agent session Close was not called")
	}
	select {
	case <-busySess.closed:
		t.Fatal("busy agent session should not be closed")
	default:
	}

	e.interactiveMu.Lock()
	_, idleStillThere := e.interactiveStates[idleKey]
	busyStill := e.interactiveStates[busyKey]
	e.interactiveMu.Unlock()
	if idleStillThere {
		t.Fatal("idle interactive state should be removed after close")
	}
	if busyStill != busyState {
		t.Fatal("busy interactive state should remain in map")
	}
	if !busySess.Alive() {
		t.Fatal("busy agent session should still be Alive")
	}
}

func TestCloseIdleAgentSessions_SkipsPendingAndQueued(t *testing.T) {
	e := newTestEngine()
	pendingKey := "test:pending"
	queuedKey := "test:queued"

	pendingSess := newControllableSession("pending-agent")
	queuedSess := newControllableSession("queued-agent")

	pendingState := &interactiveState{
		agentSession:     pendingSess,
		eventsNeedResync: false,
		pending:          &pendingPermission{Resolved: make(chan struct{})},
	}
	queuedState := &interactiveState{
		agentSession:     queuedSess,
		eventsNeedResync: false,
		pendingMessages:  []queuedMessage{{content: "queued"}},
	}

	e.interactiveMu.Lock()
	e.interactiveStates[pendingKey] = pendingState
	e.interactiveStates[queuedKey] = queuedState
	e.interactiveMu.Unlock()

	result := e.CloseIdleAgentSessions()

	if result.Closed != 0 || result.Skipped != 2 {
		t.Fatalf("result = closed=%d skipped=%d, want closed=0 skipped=2; %+v",
			result.Closed, result.Skipped, result)
	}
	if !containsString(result.SkippedSessionKeys, pendingKey) || !containsString(result.SkippedSessionKeys, queuedKey) {
		t.Fatalf("SkippedSessionKeys = %v, want both %q and %q", result.SkippedSessionKeys, pendingKey, queuedKey)
	}

	select {
	case <-pendingSess.closed:
		t.Fatal("pending session should not be closed")
	default:
	}
	select {
	case <-queuedSess.closed:
		t.Fatal("queued session should not be closed")
	default:
	}

	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()
	if e.interactiveStates[pendingKey] != pendingState || e.interactiveStates[queuedKey] != queuedState {
		t.Fatal("pending/queued interactive states should remain")
	}
}

func TestCloseIdleAgentSessions_Empty(t *testing.T) {
	e := newTestEngine()
	result := e.CloseIdleAgentSessions()
	if result.Closed != 0 || result.Skipped != 0 {
		t.Fatalf("empty result = %+v, want Closed=0 Skipped=0", result)
	}
	if len(result.ClosedSessionKeys) != 0 || len(result.SkippedSessionKeys) != 0 {
		t.Fatalf("empty keys = %+v", result)
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
