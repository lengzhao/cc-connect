# chat-api `client_flow` Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add independent MCP tool `cc_connect_client_flow` that emits non-blocking SSE `client_flow` on chat-api (type + description), without occupying the interaction slot.

**Architecture:** Same askuser HTTP MCP server gains a second tool; Hub fire-and-forget emits `EventClientFlow` into the Claude session event channel; Engine routes to optional `ClientFlowSender`; chat-api enqueues SSE using existing `newFlowID()`. Non-implementing platforms no-op; MCP still returns success.

**Tech Stack:** Go, MCP Streamable HTTP (`mcp/askuser`), Engine event loop, chat-api SSE

**Design:** `docs/plans/2026-07-23-chat-api-client-flow-design.md`

---

### Task 1: Core types + Hub emit API

**Files:**
- Modify: `core/message.go` — add `EventClientFlow`
- Modify: `core/askuser.go` — tool constants, `ClientFlow` helper, Hub method
- Modify: `core/interfaces.go` — `ClientFlowSender` + system prompt hint
- Test: `core/askuser_test.go`, `core/engine_test.go` (system prompt)

**Step 1: Write failing tests**

In `core/askuser_test.go`:

```go
func TestIsMCPClientFlowTool(t *testing.T) {
	if !IsMCPClientFlowTool(ToolCCConnectClientFlow) {
		t.Fatal("expected client flow tool")
	}
	if IsMCPClientFlowTool(ToolCCConnectAskUser) {
		t.Fatal("ask user is not client flow")
	}
}

func TestAskUserHub_EmitClientFlow(t *testing.T) {
	h := NewAskUserHub()
	var got Event
	h.Bind("s1", askEmitterFunc(func(e Event) error {
		got = e
		return nil
	}))
	if err := h.EmitClientFlow("s1", "connect_account", "绑定新账户"); err != nil {
		t.Fatal(err)
	}
	if got.Type != EventClientFlow || got.ToolName != ToolCCConnectClientFlow {
		t.Fatalf("%+v", got)
	}
	if got.ToolInputRaw["type"] != "connect_account" {
		t.Fatalf("type=%v", got.ToolInputRaw["type"])
	}
	if got.ToolInput != "绑定新账户" {
		t.Fatalf("description=%q", got.ToolInput)
	}
}

func TestAskUserHub_EmitClientFlow_InvalidType(t *testing.T) {
	h := NewAskUserHub()
	h.Bind("s1", askEmitterFunc(func(e Event) error { return nil }))
	if err := h.EmitClientFlow("s1", "account_bind", "x"); err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestAskUserHub_EmitClientFlow_NoEmitter_Succeeds(t *testing.T) {
	// Design: missing emitter → still success for non-chat-api / unbound?
	// Actually design says "session 无 emitter → MCP 错误（与 ask 一致）".
	h := NewAskUserHub()
	if err := h.EmitClientFlow("missing", "connect_account", "x"); err == nil {
		t.Fatal("expected error when no emitter")
	}
}
```

Note: **platform no-op** is Engine-side when `ClientFlowSender` missing; **no emitter** remains MCP error (session not bound).

Also extend `TestAgentSystemPrompt_GuidesAskUserQuestionSingleConfirm` or add:

```go
func TestAgentSystemPrompt_MentionsClientFlow(t *testing.T) {
	prompt := AgentSystemPrompt()
	for _, want := range []string{
		"cc_connect_client_flow",
		"mcp__ccconnect__cc_connect_client_flow",
		"connect_account",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q", want)
		}
	}
}
```

**Step 2: Run tests — expect FAIL**

```bash
go test ./core/ -run 'TestIsMCPClientFlowTool|TestAskUserHub_EmitClientFlow|TestAgentSystemPrompt_MentionsClientFlow' -v
```

**Step 3: Implement**

`core/message.go`:

```go
EventClientFlow EventType = "client_flow" // non-blocking App flow guide (SSE)
```

`core/askuser.go`:

```go
const ToolCCConnectClientFlow = "cc_connect_client_flow"
const MCPQualifiedClientFlowTool = "mcp__ccconnect__cc_connect_client_flow"

func IsMCPClientFlowTool(toolName string) bool {
	switch toolName {
	case ToolCCConnectClientFlow, MCPQualifiedClientFlowTool:
		return true
	default:
		return false
	}
}

// EmitClientFlow fire-and-forget: validates type/description, emits EventClientFlow.
// Does not wait for App. Unknown type or empty description → error.
func (h *AskUserHub) EmitClientFlow(sessionKey, flowType, description string) error {
	if h == nil {
		return fmt.Errorf("askuser hub is nil")
	}
	if sessionKey == "" {
		return fmt.Errorf("session key required")
	}
	description = strings.TrimSpace(description)
	if description == "" {
		return fmt.Errorf("description required")
	}
	flowType = NormalizeAskUserEvent(flowType)
	if flowType == "" {
		return fmt.Errorf("invalid type: must be connect_account, create_task, or task_center_approval")
	}

	evt := Event{
		Type:     EventClientFlow,
		ToolName: ToolCCConnectClientFlow,
		ToolInput: description,
		ToolInputRaw: map[string]any{
			"type":        flowType,
			"description": description,
		},
		RequestID: newAskRequestID(), // tracing only
	}

	h.mu.Lock()
	emitter, ok := h.emitters[sessionKey]
	h.mu.Unlock()
	if !ok || emitter == nil {
		return fmt.Errorf("no ask emitter for session %q", sessionKey)
	}
	return emitter.EmitAskUser(evt) // reuse AskUserEmitter channel; Type distinguishes
}
```

> Reuse `AskUserEmitter.EmitAskUser` for the channel push (Claude session already implements it). Document that `EmitAskUser` may receive either permission/ask or `EventClientFlow` events. Prefer **not** renaming unless needed.

`core/interfaces.go` — add after `AskQuestionSender`:

```go
// ClientFlowSender is optional: platforms that emit SSE client_flow guidance.
type ClientFlowSender interface {
	SendClientFlow(ctx context.Context, replyCtx any, flowType, description string) error
}
```

Append to `AgentSystemPrompt()` after ask_user section:

```text
### Client flow guide (cc_connect_client_flow)
Use mcp__ccconnect__cc_connect_client_flow when the App should open its own business flow WITHOUT a confirm card (e.g. bind new account while also asking which existing account to use). Required: type (connect_account | create_task | task_center_approval), description. Does not wait for user respond. May be used together with cc_connect_ask_user.
```

**Step 4: Run tests — expect PASS**

```bash
go test ./core/ -run 'TestIsMCPClientFlowTool|TestAskUserHub_EmitClientFlow|TestAgentSystemPrompt_MentionsClientFlow' -v
```

**Step 5: Commit**

```bash
git add core/message.go core/askuser.go core/askuser_test.go core/interfaces.go core/engine_test.go
git commit -m "feat(core): add EventClientFlow and Hub EmitClientFlow"
```

---

### Task 2: Engine routes EventClientFlow

**Files:**
- Modify: `core/engine.go` (`processInteractiveEvents` switch)
- Test: `core/engine_test.go`

**Step 1: Write failing test**

Pattern after existing interactive event tests (stub platform with `SendClientFlow`):

```go
type clientFlowPlatform struct {
	stubPlatform
	flows []struct{ typ, desc string }
}

func (p *clientFlowPlatform) SendClientFlow(_ context.Context, _ any, flowType, description string) error {
	p.flows = append(p.flows, struct{ typ, desc string }{flowType, description})
	return nil
}

func TestProcessInteractiveEvents_ClientFlow_DoesNotBlock(t *testing.T) {
	// Drive engine with EventClientFlow then EventResult;
	// assert SendClientFlow called; no pending permission; turn completes.
}
```

Also assert platform **without** `ClientFlowSender` still completes turn.

**Step 2: Run — expect FAIL**

```bash
go test ./core/ -run TestProcessInteractiveEvents_ClientFlow -v
```

**Step 3: Implement in `processInteractiveEvents`**

Add case **before** or after permission handling (non-blocking, no freeze):

```go
case EventClientFlow:
	flowType, _ := event.ToolInputRaw["type"].(string)
	desc := event.ToolInput
	if desc == "" {
		if d, ok := event.ToolInputRaw["description"].(string); ok {
			desc = d
		}
	}
	flowType = NormalizeAskUserEvent(flowType)
	if flowType == "" || strings.TrimSpace(desc) == "" {
		slog.Warn("client_flow ignored: invalid payload", "type", flowType)
		continue
	}
	if sender, ok := p.(ClientFlowSender); ok {
		if err := sender.SendClientFlow(context.Background(), replyCtx, flowType, desc); err != nil {
			slog.Warn("client_flow send failed", "error", err, "type", flowType)
		}
	} else {
		slog.Debug("client_flow skipped: platform unsupported", "type", flowType)
	}
	continue
```

Also handle `EventClientFlow` in unsolicited reader if needed: **ignore / debug log** (no active replyCtx for SSE). Prefer ignore.

**Step 4: Run — expect PASS**

```bash
go test ./core/ -run TestProcessInteractiveEvents_ClientFlow -v
```

**Step 5: Commit**

```bash
git add core/engine.go core/engine_test.go
git commit -m "feat(engine): route EventClientFlow to ClientFlowSender"
```

---

### Task 3: MCP tool `cc_connect_client_flow`

**Files:**
- Modify: `mcp/askuser/server.go` — second tool descriptor + call branch
- Modify: `mcp/askuser/parse.go` — `ParseClientFlowArguments`
- Test: `mcp/askuser/server_test.go`

**Step 1: Failing tests**

```go
func TestParseClientFlowArguments(t *testing.T) {
	in, err := ParseClientFlowArguments(json.RawMessage(`{"type":"connect_account","description":"绑定新账户"}`))
	if err != nil || in.Type != EventConnectAccount || in.Description != "绑定新账户" {
		t.Fatalf("%+v %v", in, err)
	}
	_, err = ParseClientFlowArguments(json.RawMessage(`{"type":"account_bind","description":"x"}`))
	if err == nil {
		t.Fatal("account_bind must fail")
	}
}

func TestServer_ToolsCallClientFlow(t *testing.T) {
	hub := core.NewAskUserHub()
	srv, err := Start(hub)
	// bind emitter that records EventClientFlow
	// tools/list must contain both tools
	// tools/call client_flow returns immediately with success text
}
```

**Step 2: Run — FAIL**

```bash
go test ./mcp/askuser/ -run 'TestParseClientFlow|TestServer_ToolsCallClientFlow' -v
```

**Step 3: Implement**

`parse.go`:

```go
type ClientFlowArgs struct {
	Type        string
	Description string
}

func ParseClientFlowArguments(raw json.RawMessage) (ClientFlowArgs, error) {
	// require type + description; NormalizeEvent; reject empty normalized type
}
```

`server.go`:

- `tools/list` → `{"tools": []any{toolDescriptor(), clientFlowToolDescriptor()}}`
- `tools/call` → dispatch by name (`ToolCCConnectAskUser` vs `ToolCCConnectClientFlow` / qualified names)
- `clientFlowToolDescriptor`: required `type`,`description`; enum same three values (**no empty string** in enum)

```go
func (s *Server) callClientFlow(ctx context.Context, sessionKey string, argsRaw json.RawMessage) (any, error) {
	in, err := ParseClientFlowArguments(argsRaw)
	if err != nil {
		return nil, err
	}
	if err := s.hub.EmitClientFlow(sessionKey, in.Type, in.Description); err != nil {
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "client_flow failed: " + err.Error()}},
			"isError": true,
		}, nil
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("client_flow emitted: %s", in.Type)}},
	}, nil
}
```

**Step 4: PASS + commit**

```bash
go test ./mcp/askuser/ -v
git add mcp/askuser/
git commit -m "feat(askuser): add MCP tool cc_connect_client_flow"
```

---

### Task 4: chat-api `SendClientFlow` SSE

**Files:**
- Modify: `platform/chat-api/interaction.go`
- Modify: `platform/chat-api/chatapi.go` (interface assert if present)
- Test: `platform/chat-api/interaction_test.go`

**Step 1: Failing test**

```go
func TestSendClientFlow_EmitsSSEWithoutInteraction(t *testing.T) {
	// similar to TestSendAskQuestion_RichSingleConfirm but call SendClientFlow
	// assert event name "client_flow"
	// payload: flow_id prefix "flow_", type, description, run_id, message_id
	// assert NO interaction_id / expires_at
	// assert run.interaction remains nil (or previous question untouched)
}

func TestSendClientFlow_CoexistsWithQuestionRequest(t *testing.T) {
	// SendAskQuestion then SendClientFlow (or reverse)
	// both SSE present; interaction still question
}
```

**Step 2: FAIL**

```bash
go test ./platform/chat-api/ -run TestSendClientFlow -v
```

**Step 3: Implement**

```go
func (p *Platform) SendClientFlow(_ context.Context, replyTo any, flowType, description string) error {
	rc, ok := replyTo.(*replyContext)
	if !ok || rc == nil || rc.runID == "" {
		return fmt.Errorf("chat-api: unsupported reply context %T", replyTo)
	}
	flowType = core.NormalizeAskUserEvent(flowType)
	description = strings.TrimSpace(description)
	if flowType == "" || description == "" {
		return fmt.Errorf("chat-api: invalid client_flow")
	}
	run := p.pending.get(rc.runID)
	if run == nil {
		return fmt.Errorf("chat-api: run %q is not pending", rc.runID)
	}
	run.enqueueEvent("client_flow", map[string]any{
		"flow_id":     newFlowID(),
		"type":        flowType,
		"description": description,
		"run_id":      run.id,
		"message_id":  run.messageID,
	})
	return nil
}

var _ core.ClientFlowSender = (*Platform)(nil)
```

Optional: debug UI log line for `client_flow` (nice-to-have; not required for green tests).

**Step 4: PASS + commit**

```bash
go test ./platform/chat-api/ -run 'TestSendClientFlow|TestSendAskQuestion' -v
git add platform/chat-api/
git commit -m "feat(chat-api): emit SSE client_flow via ClientFlowSender"
```

---

### Task 5: Docs + CHANGELOG

**Files:**
- Modify: `docs/chat-api.zh-CN.md` — § SSE events + §3.x `client_flow`
- Modify: `docs/chat-api.md` — short mention + link
- Modify: `logs/agent-chat-api.zh-CN.md` — fix `account_bind` → `connect_account`; list type enum
- Modify: `CHANGELOG.md`
- Modify: `docs/plans/2026-07-23-chat-api-client-flow-design.md` — Status: implemented
- Optionally update `docs/plans/2026-07-22-askuserquestion-rich-confirm-design.md` non-goal note to point at client_flow design

**Content checklist for zh-CN:**

- Overview bullet: 极简 `client_flow`
- Event table row
- Full subsection mirroring external contract
- State machine / client guide: 不阻塞 interaction
- Note: Agent 来源 MCP `cc_connect_client_flow`

**Step: Commit**

```bash
git add docs/ CHANGELOG.md logs/agent-chat-api.zh-CN.md
git commit -m "docs: document chat-api client_flow SSE and MCP tool"
```

---

### Task 6: Full verification

```bash
go test ./core/ ./mcp/askuser/ ./platform/chat-api/ -count=1
go test ./core/ -run TestCUJ -count=1
go build ./...
```

Fix any fallout. Final commit only if needed.

---

## Execution notes

- TDD per task; do not skip failing-test step.
- Keep `core/` free of platform names.
- Do not change `cc_connect_ask_user` options requirement or `question_request.event` behavior.
- Reuse `NormalizeAskUserEvent` for type validation (empty after normalize = invalid for client_flow).
