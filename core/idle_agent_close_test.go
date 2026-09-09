package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubIdleCloserBinderPlatform is a minimal Platform that records the
// IdleAgentSessionCloser bound during Engine.Start.
type stubIdleCloserBinderPlatform struct {
	n      string
	closer IdleAgentSessionCloser
}

func (p *stubIdleCloserBinderPlatform) Name() string               { return p.n }
func (p *stubIdleCloserBinderPlatform) Start(MessageHandler) error { return nil }
func (p *stubIdleCloserBinderPlatform) Stop() error                { return nil }
func (p *stubIdleCloserBinderPlatform) Reply(context.Context, any, string) error {
	return nil
}
func (p *stubIdleCloserBinderPlatform) Send(context.Context, any, string) error {
	return nil
}
func (p *stubIdleCloserBinderPlatform) BindIdleAgentSessionCloser(c IdleAgentSessionCloser) {
	p.closer = c
}

func TestEngineStart_BindsIdleAgentSessionCloser(t *testing.T) {
	p := &stubIdleCloserBinderPlatform{n: "chat-api"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p.closer == nil {
		t.Fatal("BindIdleAgentSessionCloser was not called")
	}
	if p.closer != e {
		t.Fatalf("bound closer = %T, want *Engine", p.closer)
	}
}

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

// TestCloseIdleAgentSessions_WorkspacePrefixedBusySkipped ensures Busy is detected
// when the interactive key uses a non-normalized workspaceDir prefix (exact trim)
// and the Session was registered under the raw session key.
func TestCloseIdleAgentSessions_WorkspacePrefixedBusySkipped(t *testing.T) {
	e := newTestEngine()

	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	// Trailing separator so exact prefix != normalizeWorkspacePath(workspaceDir)+":".
	wsDir := proj + string(os.PathSeparator)
	sessionKey := "test:ws-busy"
	interactiveKey := wsDir + ":" + sessionKey

	if normalizeWorkspacePath(wsDir)+":" == wsDir+":" {
		t.Fatalf("test setup: expected non-normalized wsDir %q to differ from normalize %q",
			wsDir, normalizeWorkspacePath(wsDir))
	}

	agentSess := newControllableSession("ws-busy-agent")
	state := &interactiveState{
		agentSession:     agentSess,
		eventsNeedResync: false,
		workspaceDir:     wsDir,
	}

	e.interactiveMu.Lock()
	e.interactiveStates[interactiveKey] = state
	e.interactiveMu.Unlock()

	busySession := e.sessions.GetOrCreateActive(sessionKey)
	if !busySession.TryLock() {
		t.Fatal("expected TryLock to succeed")
	}
	t.Cleanup(func() { busySession.Unlock() })

	result := e.CloseIdleAgentSessions()

	if result.Closed != 0 || result.Skipped != 1 {
		t.Fatalf("result = closed=%d skipped=%d, want closed=0 skipped=1; %+v",
			result.Closed, result.Skipped, result)
	}
	if !containsString(result.SkippedSessionKeys, interactiveKey) {
		t.Fatalf("SkippedSessionKeys = %v, want %q", result.SkippedSessionKeys, interactiveKey)
	}

	select {
	case <-agentSess.closed:
		t.Fatal("busy workspace-prefixed session should not be closed")
	default:
	}

	e.interactiveMu.Lock()
	still := e.interactiveStates[interactiveKey]
	e.interactiveMu.Unlock()
	if still != state {
		t.Fatal("interactive state should remain when Busy")
	}
	if !agentSess.Alive() {
		t.Fatal("agent session should still be Alive")
	}
}

// TestCloseIdleAgentSessions_BusyUnderFullInteractiveKey covers custom command/skill
// paths that register the Session under the full workspace-prefixed interactive key.
func TestCloseIdleAgentSessions_BusyUnderFullInteractiveKey(t *testing.T) {
	e := newTestEngine()

	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	wsDir := proj
	sessionKey := "test:full-key-busy"
	interactiveKey := wsDir + ":" + sessionKey

	agentSess := newControllableSession("full-key-busy-agent")
	state := &interactiveState{
		agentSession:     agentSess,
		eventsNeedResync: false,
		workspaceDir:     wsDir,
	}

	e.interactiveMu.Lock()
	e.interactiveStates[interactiveKey] = state
	e.interactiveMu.Unlock()

	busySession := e.sessions.GetOrCreateActive(interactiveKey)
	if !busySession.TryLock() {
		t.Fatal("expected TryLock to succeed")
	}
	t.Cleanup(func() { busySession.Unlock() })

	result := e.CloseIdleAgentSessions()

	if result.Closed != 0 || result.Skipped != 1 {
		t.Fatalf("result = closed=%d skipped=%d, want closed=0 skipped=1; %+v",
			result.Closed, result.Skipped, result)
	}
	if !containsString(result.SkippedSessionKeys, interactiveKey) {
		t.Fatalf("SkippedSessionKeys = %v, want %q", result.SkippedSessionKeys, interactiveKey)
	}

	select {
	case <-agentSess.closed:
		t.Fatal("busy session under full interactive key should not be closed")
	default:
	}
}

func TestCloseIdleAgentSessions_PreservesAgentSessionID(t *testing.T) {
	e := newTestEngine()
	key := "test:preserve-asid"

	agentSess := newControllableSession("preserve-asid-agent")
	state := &interactiveState{agentSession: agentSess, eventsNeedResync: false}

	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	sess := e.sessions.GetOrCreateActive(key)
	sess.SetAgentSessionID("as_keep", "stub")

	result := e.CloseIdleAgentSessions()
	if result.Closed != 1 || result.Skipped != 0 {
		t.Fatalf("result = closed=%d skipped=%d, want closed=1 skipped=0; %+v",
			result.Closed, result.Skipped, result)
	}

	select {
	case <-agentSess.closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("idle agent session Close was not called")
	}

	if got := sess.GetAgentSessionID(); got != "as_keep" {
		t.Fatalf("AgentSessionID = %q, want %q", got, "as_keep")
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

// TestCloseIdleAgentSessions_BusySessionInEngineStoreWithWorkspacePool is the
// regression test for the production hang: a chat-api turn was killed mid-flight
// by close-idle and its SSE run never received a terminal event.
//
// chat-api returns UseWorkspaceSessionStore() == false, so handleMessage locks
// the Session in the ENGINE-level store even though the turn runs inside a
// workspace. sessionBusyForInteractiveKey used to replace its lookup store with
// ws.sessions whenever the workspace pool had an entry, so it searched the wrong
// map, found nothing, and reported the busy session as idle.
func TestCloseIdleAgentSessions_BusySessionInEngineStoreWithWorkspacePool(t *testing.T) {
	e := newTestEngine()

	wsDir := filepath.Join(t.TempDir(), "nex-training")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionKey := "chat-api:nex-training:conv_busy"
	interactiveKey := wsDir + ":" + sessionKey

	// A per-workspace session store exists and is deliberately EMPTY, mirroring
	// multi-workspace mode with a platform that opts out of it.
	e.workspacePool = newWorkspacePool(DefaultWorkspaceIdleTimeout)
	ws := e.workspacePool.GetOrCreate(wsDir)
	ws.sessions = NewSessionManager(filepath.Join(t.TempDir(), "ws_sessions.json"))

	agentSess := newControllableSession("chat-api-busy-agent")
	state := &interactiveState{
		agentSession:     agentSess,
		eventsNeedResync: false,
		workspaceDir:     wsDir,
	}
	e.interactiveMu.Lock()
	e.interactiveStates[interactiveKey] = state
	e.interactiveMu.Unlock()

	// The in-flight turn holds the lock in the engine-level store.
	busySession := e.sessions.GetOrCreateActive(sessionKey)
	if !busySession.TryLock() {
		t.Fatal("expected TryLock to succeed")
	}
	t.Cleanup(func() { busySession.Unlock() })

	if !e.sessionBusyForInteractiveKey(interactiveKey, wsDir) {
		t.Fatal("sessionBusyForInteractiveKey missed a busy session held in the engine-level store")
	}

	result := e.CloseIdleAgentSessions()
	if result.Closed != 0 || result.Skipped != 1 {
		t.Fatalf("result = closed=%d skipped=%d, want closed=0 skipped=1; %+v",
			result.Closed, result.Skipped, result)
	}
	select {
	case <-agentSess.closed:
		t.Fatal("close-idle killed a session with a turn in flight")
	default:
	}
	if !agentSess.Alive() {
		t.Fatal("agent session should still be Alive")
	}
}

// A busy session recorded in the per-workspace store must still be protected —
// checking both stores must not regress platforms that do use the workspace one.
func TestCloseIdleAgentSessions_BusySessionInWorkspaceStore(t *testing.T) {
	e := newTestEngine()

	wsDir := filepath.Join(t.TempDir(), "lark-chat")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionKey := "lark:oc_abc:root"
	interactiveKey := wsDir + ":" + sessionKey

	e.workspacePool = newWorkspacePool(DefaultWorkspaceIdleTimeout)
	ws := e.workspacePool.GetOrCreate(wsDir)
	ws.sessions = NewSessionManager(filepath.Join(t.TempDir(), "ws_sessions.json"))

	agentSess := newControllableSession("lark-busy-agent")
	e.interactiveMu.Lock()
	e.interactiveStates[interactiveKey] = &interactiveState{
		agentSession:     agentSess,
		eventsNeedResync: false,
		workspaceDir:     wsDir,
	}
	e.interactiveMu.Unlock()

	busySession := ws.sessions.GetOrCreateActive(sessionKey)
	if !busySession.TryLock() {
		t.Fatal("expected TryLock to succeed")
	}
	t.Cleanup(func() { busySession.Unlock() })

	result := e.CloseIdleAgentSessions()
	if result.Closed != 0 || result.Skipped != 1 {
		t.Fatalf("result = closed=%d skipped=%d, want closed=0 skipped=1", result.Closed, result.Skipped)
	}
	if !agentSess.Alive() {
		t.Fatal("agent session should still be Alive")
	}
}
