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

// SessionKeyHeader routes MCP tools/call to the correct agent session.
const SessionKeyHeader = "X-CC-Session-Key"

// Known AskUser envelope events that guide App page navigation.
const (
	AskEventConnectAccount     = "connect_account"
	AskEventCreateTask         = "create_task"
	AskEventTaskCenterApproval = "task_center_approval"
)

// NormalizeAskUserEvent keeps only known navigation events.
// Empty / null / unmatched → "" (no extra App action button; generic confirm).
func NormalizeAskUserEvent(raw string) string {
	switch strings.TrimSpace(raw) {
	case AskEventConnectAccount, AskEventCreateTask, AskEventTaskCenterApproval:
		return strings.TrimSpace(raw)
	default:
		return ""
	}
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

// AskUserResult is the outcome of a Hub-mediated ask.
type AskUserResult struct {
	Answers        map[int]string // agent-facing values by question index
	DisplayAnswers map[int]string
	Denied         bool
	Message        string
}

// AskUserEmitter delivers a permission-style ask event into a live session.
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
