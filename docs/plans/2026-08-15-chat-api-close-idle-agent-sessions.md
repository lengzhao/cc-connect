# Close Idle Agent Sessions Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add `POST /v1/agent-sessions/close-idle` so chat-api can close all idle live agent processes in the project Engine without touching busy sessions.

**Architecture:** `Engine.CloseIdleAgentSessions()` implements the closable predicate (aligned with `agent_session_idle_timeout` + `Session.Busy()`), reuses idle-timeout cleanup semantics, and is bound into chat-api via optional `IdleAgentSessionCloserBinder` (same pattern as `SessionManagerBinder`).

**Tech Stack:** Go, `platform/chat-api` HTTP, `core.Engine` interactiveStates

**Design:** [2026-08-15-chat-api-close-idle-agent-sessions-design.md](./2026-08-15-chat-api-close-idle-agent-sessions-design.md)

**TDD:** Every task writes a failing test first. @test-driven-development

---

### Task 1: Core result type + closer interfaces

**Files:**
- Modify: `core/interfaces.go` (near `SessionManagerBinder`)
- Create/Modify: `core/idle_agent_close.go` (new) or add types next to binder in `interfaces.go`

**Step 1: Write the failing compile/test stub**

Add to `core/idle_agent_close_test.go`:

```go
package core

import "testing"

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
```

**Step 2: Run test — expect FAIL (type undefined)**

```bash
go test ./core/ -run TestCloseIdleAgentSessionsResult_Fields -count=1
```

**Step 3: Minimal types**

In `core/interfaces.go` (after `SessionManagerBinder`):

```go
// CloseIdleAgentSessionsResult is returned by IdleAgentSessionCloser.
type CloseIdleAgentSessionsResult struct {
	Closed             int
	Skipped            int
	ClosedSessionKeys  []string
	SkippedSessionKeys []string
}

// IdleAgentSessionCloser closes live agent processes that are idle
// (not mid-turn / permission / queued). Implemented by Engine.
type IdleAgentSessionCloser interface {
	CloseIdleAgentSessions() CloseIdleAgentSessionsResult
}

// IdleAgentSessionCloserBinder is optional; Engine.Start binds the closer.
type IdleAgentSessionCloserBinder interface {
	BindIdleAgentSessionCloser(c IdleAgentSessionCloser)
}
```

**Step 4: Run test — PASS**

**Step 5: Commit**

```bash
git add core/interfaces.go core/idle_agent_close_test.go
git commit -m "feat(core): add IdleAgentSessionCloser interfaces"
```

---

### Task 2: SessionManager.GetActive (Busy lookup without create)

**Files:**
- Modify: `core/session.go`
- Modify: `core/session_test.go`

**Step 1: Failing test**

```go
func TestSessionManager_GetActive_NoCreate(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionManager(filepath.Join(dir, "s.json"))
	if got := sm.GetActive("missing"); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
	s := sm.GetOrCreateActive("u1")
	got := sm.GetActive("u1")
	if got != s {
		t.Fatalf("GetActive = %v, want same session", got)
	}
}
```

**Step 2: Run — FAIL**

```bash
go test ./core/ -run TestSessionManager_GetActive_NoCreate -count=1
```

**Step 3: Implement**

```go
// GetActive returns the active session for userKey, or nil if none.
func (sm *SessionManager) GetActive(userKey string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sid, ok := sm.activeSession[userKey]
	if !ok {
		return nil
	}
	return sm.sessions[sid]
}
```

**Step 4: PASS + commit**

```bash
git commit -m "feat(core): SessionManager.GetActive without create"
```

---

### Task 3: Engine.CloseIdleAgentSessions — close idle, skip busy

**Files:**
- Modify: `core/engine.go` (new methods near idle-close helpers ~4300)
- Modify: `core/engine_test.go` (or new `core/idle_agent_close_test.go`)

**Step 1: Failing tests** (use existing controllable/stub agent session patterns from `engine_test.go`)

```go
func TestCloseIdleAgentSessions_ClosesIdlePreservesBusy(t *testing.T) {
	// Setup Engine with two interactiveStates:
	//  - idleKey: Alive, no pending, no queue, session !Busy
	//  - busyKey: Alive, session.Busy()==true (TryLock already held)
	// Call e.CloseIdleAgentSessions()
	// Assert: idle closed (Close called, removed from map); busy still Alive in map
	// Assert result.Closed==1, Skipped==1, keys match
}

func TestCloseIdleAgentSessions_SkipsPendingAndQueued(t *testing.T) {
	// pending != nil → skipped
	// pendingMessages non-empty → skipped
}

func TestCloseIdleAgentSessions_Empty(t *testing.T) {
	// no states → Closed==0, Skipped==0
}
```

Use `controllableAgentSession` / stubs already in `engine_test.go`. Follow `Test*agentSessionIdle*` tests around idle close for setup patterns (`agentSessionIdleToken`, etc.).

**Step 2: Run — FAIL**

```bash
go test ./core/ -run TestCloseIdleAgentSessions_ -count=1
```

**Step 3: Implement `CloseIdleAgentSessions`**

Logic sketch:

```go
func (e *Engine) CloseIdleAgentSessions() CloseIdleAgentSessionsResult {
	var result CloseIdleAgentSessionsResult
	e.interactiveMu.Lock()
	keys := make([]string, 0, len(e.interactiveStates))
	for k := range e.interactiveStates {
		keys = append(keys, k)
	}
	e.interactiveMu.Unlock()

	for _, key := range keys {
		closed, skipped := e.tryCloseIdleAgentSession(key)
		if closed {
			result.Closed++
			result.ClosedSessionKeys = append(result.ClosedSessionKeys, key)
		} else if skipped {
			result.Skipped++
			result.SkippedSessionKeys = append(result.SkippedSessionKeys, key)
		}
	}
	return result
}
```

`tryCloseIdleAgentSession`:
1. Lock `interactiveMu` + `state.mu`
2. If no state / no Alive agent → return (false, false) — ignore
3. If not closable (stopped, eventsNeedResync, pending, queue, or `sessionBusyForInteractiveKey`) → return (false, true)
4. Else: same cleanup as `cleanupInteractiveStateForIdleToken` (cancel idle timer, nil agentSession, unlock, stop unsolicited, Close, delete from map)
5. Log: `slog.Info("close idle agent sessions: closing", "session_key", key)`

`sessionBusyForInteractiveKey(key, workspaceDir string)`:
- Prefer exact prefix `workspaceDir + ":"` when trimming the interactive key; fallback to `normalizeWorkspacePath(workspaceDir) + ":"`
- Resolve SessionManager: workspace pool sessions for `workspaceDir`, else `e.sessions`
- `GetActive` on trimmed raw key (when trim succeeded) **and** full interactive `key`; return true if **any** found session is `Busy()`
- Caller copies `workspaceDir` under `state.mu`, then releases `state.mu` before this call (avoid holding `state.mu` across Session locks); keep `interactiveMu` as needed for map safety

Extract shared close body with idle-timeout path if easy (DRY); otherwise duplicate carefully to avoid behavior drift — prefer calling a shared `closeLiveIdleAgentSession(sessionKey, expected *interactiveState)` used by both idle timer and this API.

**Step 4: PASS**

```bash
go test ./core/ -run TestCloseIdleAgentSessions_ -count=1
```

**Step 5: Commit**

```bash
git commit -m "feat(core): CloseIdleAgentSessions closes idle live agents only"
```

---

### Task 4: Bind closer on Engine.Start

**Files:**
- Modify: `core/engine.go` (`Start`, next to `SessionManagerBinder`)
- Modify: `core/engine_test.go` or chat-api test later — optional core test with stub binder

**Step 1: Failing test** — stub platform implementing `IdleAgentSessionCloserBinder`; after `Start`, assert binder received non-nil closer.

**Step 2: In `Start` loop:**

```go
if binder, ok := p.(IdleAgentSessionCloserBinder); ok {
	binder.BindIdleAgentSessionCloser(e)
}
```

**Step 3: PASS + commit**

```bash
git commit -m "feat(core): bind IdleAgentSessionCloser on Engine.Start"
```

---

### Task 5: chat-api binder + HTTP endpoint

**Files:**
- Modify: `platform/chat-api/chatapi.go` (field, `routes`, compile-time assert)
- Create: `platform/chat-api/close_idle.go`
- Create: `platform/chat-api/close_idle_test.go`

**Step 1: Failing HTTP tests**

```go
func TestCloseIdleAgentSessions_OK(t *testing.T) { /* mock closer returns closed=2 skipped=1; POST; assert JSON */ }
func TestCloseIdleAgentSessions_Unbound_503(t *testing.T) { /* no binder; 503 */ }
func TestCloseIdleAgentSessions_MethodNotAllowed(t *testing.T) { /* GET → 405 */ }
func TestCloseIdleAgentSessions_NoChannelRequired(t *testing.T) { /* POST without channel header → 200 */ }
```

Mock:

```go
type stubIdleCloser struct {
	result core.CloseIdleAgentSessionsResult
}

func (s *stubIdleCloser) CloseIdleAgentSessions() core.CloseIdleAgentSessionsResult {
	return s.result
}
```

Wire via `p.BindIdleAgentSessionCloser(stub)`.

**Step 2: Run — FAIL**

```bash
go test ./platform/chat-api/ -run TestCloseIdleAgentSessions_ -count=1
```

**Step 3: Implement**

`Platform` fields:

```go
idleCloser   core.IdleAgentSessionCloser
idleCloserMu sync.RWMutex
```

```go
func (p *Platform) BindIdleAgentSessionCloser(c core.IdleAgentSessionCloser) {
	p.idleCloserMu.Lock()
	defer p.idleCloserMu.Unlock()
	p.idleCloser = c
}
```

Route:

```go
mux.HandleFunc(p.path+"agent-sessions/close-idle", wrap(p.handleCloseIdleAgentSessions))
```

Handler: auth only (existing wrap); **do not** call `resolveChannel`; call closer; `writeOK` with JSON fields `closed`, `skipped`, `closed_session_keys`, `skipped_session_keys`.

```go
var _ core.IdleAgentSessionCloserBinder = (*Platform)(nil)
```

**Step 4: PASS + commit**

```bash
git commit -m "feat(chat-api): POST /agent-sessions/close-idle"
```

---

### Task 6: Docs

**Files:**
- Modify: `docs/chat-api.zh-CN.md`
- Modify: `docs/chat-api.md`

**Updates:**
1. Endpoint table in §5 — add `POST /agent-sessions/close-idle`
2. Auth/channel section — note this endpoint does **not** require `X-Chat-API-Channel`
3. Short subsection: behavior, response fields, link to design doc
4. Changelog row at bottom of zh-CN/EN if present

**Step 1:** Edit docs  
**Step 2:** Commit

```bash
git commit -m "docs(chat-api): document close-idle agent sessions endpoint"
```

---

### Task 7: Full verification

```bash
go test ./core/ -run 'TestCloseIdleAgentSessions_|TestSessionManager_GetActive' -count=1
go test ./platform/chat-api/ -run TestCloseIdleAgentSessions_ -count=1
go test ./core/ ./platform/chat-api/ -count=1
```

Fix any fallout. Confirm design doc Status remains `approved` (or set `implemented` if you update it).

---

## Execution notes

- Prefer extracting shared `closeLiveIdleAgentSession` used by idle timer + API to avoid drift.
- Do not clear `AgentSessionID`.
- Do not dispatch `/stop`.
- Interactive map keys may be `workspace:sessionKey`; include full key in response as stored in `interactiveStates`.
