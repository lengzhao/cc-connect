package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Canonical tool name for cc-connect structured ask (MCP path).
const ToolCCConnectAskUser = "cc_connect_ask_user"

// MCPQualifiedAskUserTool is the Claude Code MCP tool name for AskUser.
const MCPQualifiedAskUserTool = "mcp__ccconnect__cc_connect_ask_user"

// ToolCCConnectClientFlow is the canonical MCP tool for non-blocking App flow guides.
const ToolCCConnectClientFlow = "cc_connect_client_flow"

// MCPQualifiedClientFlowTool is the Claude Code MCP tool name for client_flow.
const MCPQualifiedClientFlowTool = "mcp__ccconnect__cc_connect_client_flow"

// SessionKeyHeader routes MCP tools/call to the correct agent session.
const SessionKeyHeader = "X-CC-Session-Key"

// NormalizeAskUserEvent trims navigation / client_flow event strings.
// Values are passed through as-is (no allowlist); empty after trim → "".
func NormalizeAskUserEvent(raw string) string {
	return strings.TrimSpace(raw)
}

// IsStructuredAsk reports whether an event should use the AskUserQuestion UI
// flow. Capability-driven: any event carrying Questions is a structured ask.
func IsStructuredAsk(event Event) bool {
	return len(event.Questions) > 0
}

// IsMCPAskTool reports whether the tool completes via AskUserHub (tool result)
// rather than Claude control_response.
func IsMCPAskTool(toolName string) bool {
	switch toolName {
	case ToolCCConnectAskUser, MCPQualifiedAskUserTool:
		return true
	default:
		return false
	}
}

// IsMCPClientFlowTool reports whether the tool is the fire-and-forget client_flow MCP.
func IsMCPClientFlowTool(toolName string) bool {
	switch toolName {
	case ToolCCConnectClientFlow, MCPQualifiedClientFlowTool:
		return true
	default:
		return false
	}
}

// AskUserResult is the outcome of a Hub-mediated ask.
type AskUserResult struct {
	Answers        map[int]string // agent-facing values by question index
	DisplayAnswers map[int]string
	Denied         bool
	Message        string
}

// AskUserEmitter delivers a Hub-mediated event into a live session.
// Events may be structured asks (permission_request) or non-blocking
// client_flow guides; Type distinguishes them.
type AskUserEmitter interface {
	EmitAskUser(event Event) error
}

// AskUserHub binds session keys to emitters and completes MCP ask waiters.
type AskUserHub struct {
	mu       sync.Mutex
	emitters map[string]AskUserEmitter
	pending  map[string]*askWaiter // requestID -> waiter
}

type askWaiter struct {
	sessionKey string
	ch         chan AskUserResult
}

// NewAskUserHub creates an empty hub.
func NewAskUserHub() *AskUserHub {
	return &AskUserHub{
		emitters: make(map[string]AskUserEmitter),
		pending:  make(map[string]*askWaiter),
	}
}

// Bind registers an emitter for sessionKey (replaces previous).
func (h *AskUserHub) Bind(sessionKey string, emitter AskUserEmitter) {
	if h == nil || sessionKey == "" || emitter == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.emitters[sessionKey] = emitter
}

// Unbind removes the emitter and cancels pending asks for the session.
func (h *AskUserHub) Unbind(sessionKey string) {
	if h == nil || sessionKey == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.emitters, sessionKey)
	for id, w := range h.pending {
		if w.sessionKey == sessionKey {
			select {
			case w.ch <- AskUserResult{Denied: true, Message: "session unbound"}:
			default:
			}
			delete(h.pending, id)
		}
	}
}

// Ask emits a structured ask event and blocks until Complete/Deny or ctx done.
func (h *AskUserHub) Ask(ctx context.Context, sessionKey string, q UserQuestion) (AskUserResult, error) {
	if h == nil {
		return AskUserResult{}, fmt.Errorf("askuser hub is nil")
	}
	if sessionKey == "" {
		return AskUserResult{}, fmt.Errorf("session key required")
	}
	if q.Question == "" {
		return AskUserResult{}, fmt.Errorf("question required")
	}

	reqID := newAskRequestID()
	q.Event = NormalizeAskUserEvent(q.Event)
	raw := map[string]any{
		"question":           q.Question,
		"description":        q.Description,
		"event":              q.Event,
		"allow_custom_input": q.AllowCustomInput,
		"multi_select":       q.MultiSelect,
		"options":            optionsToRaw(q.Options),
	}
	evt := Event{
		Type:         EventPermissionRequest,
		RequestID:    reqID,
		ToolName:     ToolCCConnectAskUser,
		ToolInput:    q.Question,
		ToolInputRaw: raw,
		Questions:    []UserQuestion{q},
	}

	w := &askWaiter{
		sessionKey: sessionKey,
		ch:         make(chan AskUserResult, 1),
	}

	h.mu.Lock()
	emitter, ok := h.emitters[sessionKey]
	if !ok || emitter == nil {
		h.mu.Unlock()
		return AskUserResult{}, fmt.Errorf("no ask emitter for session %q", sessionKey)
	}
	h.pending[reqID] = w
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.pending, reqID)
		h.mu.Unlock()
	}()

	if err := emitter.EmitAskUser(evt); err != nil {
		return AskUserResult{}, err
	}

	select {
	case res := <-w.ch:
		return res, nil
	case <-ctx.Done():
		return AskUserResult{}, ctx.Err()
	}
}

// EmitClientFlow fire-and-forget: validates type/description, emits EventClientFlow.
// Does not wait for App respond. Unknown type or empty description → error.
// args is optional; non-empty values are forwarded to the App via SSE.
func (h *AskUserHub) EmitClientFlow(sessionKey, flowType, description, args string) error {
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
		return fmt.Errorf("type required")
	}

	args = strings.TrimSpace(args)
	raw := map[string]any{
		"type":        flowType,
		"description": description,
	}
	if args != "" {
		raw["args"] = args
	}
	evt := Event{
		Type:         EventClientFlow,
		ToolName:     ToolCCConnectClientFlow,
		ToolInput:    description,
		ToolInputRaw: raw,
		RequestID:    newAskRequestID(),
	}

	h.mu.Lock()
	emitter, ok := h.emitters[sessionKey]
	h.mu.Unlock()
	if !ok || emitter == nil {
		return fmt.Errorf("no ask emitter for session %q", sessionKey)
	}
	return emitter.EmitAskUser(evt)
}

// Complete resolves a pending MCP ask with collected answers.
func (h *AskUserHub) Complete(requestID string, answers, display map[int]string) bool {
	if h == nil || requestID == "" {
		return false
	}
	h.mu.Lock()
	w, ok := h.pending[requestID]
	if ok {
		delete(h.pending, requestID)
	}
	h.mu.Unlock()
	if !ok || w == nil {
		return false
	}
	res := AskUserResult{Answers: answers, DisplayAnswers: display}
	select {
	case w.ch <- res:
	default:
	}
	return true
}

// Deny resolves a pending MCP ask as denied.
func (h *AskUserHub) Deny(requestID, message string) bool {
	if h == nil || requestID == "" {
		return false
	}
	h.mu.Lock()
	w, ok := h.pending[requestID]
	if ok {
		delete(h.pending, requestID)
	}
	h.mu.Unlock()
	if !ok || w == nil {
		return false
	}
	select {
	case w.ch <- AskUserResult{Denied: true, Message: message}:
	default:
	}
	return true
}

func newAskRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "ask_" + hex.EncodeToString(b[:]) + fmt.Sprintf("_%d", time.Now().UnixNano()%1e6)
}

func optionsToRaw(opts []UserQuestionOption) []any {
	out := make([]any, 0, len(opts))
	for _, o := range opts {
		m := map[string]any{"label": o.Label}
		if o.Description != "" {
			m["description"] = o.Description
		}
		if o.Value != "" {
			m["value"] = o.Value
		}
		if o.Tag != "" {
			tag := map[string]any{"text": o.Tag}
			if o.TagVariant != "" {
				tag["variant"] = o.TagVariant
			}
			m["tag"] = tag
		}
		out = append(out, m)
	}
	return out
}
