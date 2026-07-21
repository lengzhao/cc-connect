package core

import (
	"context"
	"strings"
)

// TurnToolCall is one tool invocation in display order (Index is 1-based).
type TurnToolCall struct {
	Index  int
	Name   string
	Input  string
	Result *TurnToolResult // nil until result arrives
}

// TurnToolResult holds the outcome of a tool invocation.
type TurnToolResult struct {
	Output   string
	Status   string
	ExitCode *int
	Success  *bool
}

// TurnStreamSnapshot is a consistent full view of an in-progress or finished turn.
type TurnStreamSnapshot struct {
	Thinking string
	Answer   string
	Tools    []TurnToolCall
}

// TurnStreamEventKind distinguishes typed turn-stream deltas.
type TurnStreamEventKind int

const (
	TurnStreamThinkingReplace TurnStreamEventKind = iota
	TurnStreamAnswerAppend
	TurnStreamAnswerReplace
	TurnStreamToolUpsert
	TurnStreamToolResult
)

// TurnStreamEvent is one typed update for StructuredStreamingCard consumers.
type TurnStreamEvent struct {
	Kind     TurnStreamEventKind
	Thinking string       // ThinkingReplace: full thinking text
	Answer   string       // Append: suffix; Replace: full answer
	Tool     TurnToolCall // Upsert / Result (Result fills Tool.Result)
}

// turnStreamEmitter owns streaming-card turn state and dual-writes:
//   - always markdown via StreamingCard.Update (Phase 1; DingTalk/Slack forever)
//   - optional typed events when the card implements StructuredStreamingCard
type turnStreamEmitter struct {
	card     StreamingCard
	structed StructuredStreamingCard // nil if card does not implement
	thinking string
	tools    []TurnToolCall
	answer   strings.Builder
}

func newTurnStreamEmitter(card StreamingCard) *turnStreamEmitter {
	if card == nil {
		return nil
	}
	e := &turnStreamEmitter{card: card}
	if ssc, ok := card.(StructuredStreamingCard); ok {
		e.structed = ssc
	}
	return e
}

func (e *turnStreamEmitter) Failed() bool {
	return e == nil || e.card == nil || e.card.Failed()
}

func (e *turnStreamEmitter) Thinking() string {
	if e == nil {
		return ""
	}
	return e.thinking
}

func (e *turnStreamEmitter) Answer() string {
	if e == nil {
		return ""
	}
	return e.answer.String()
}

func (e *turnStreamEmitter) Snapshot() TurnStreamSnapshot {
	if e == nil {
		return TurnStreamSnapshot{}
	}
	tools := make([]TurnToolCall, len(e.tools))
	copy(tools, e.tools)
	return TurnStreamSnapshot{
		Thinking: e.thinking,
		Answer:   e.answer.String(),
		Tools:    tools,
	}
}

func (e *turnStreamEmitter) cardTools() []cardToolEntry {
	if e == nil {
		return nil
	}
	out := make([]cardToolEntry, len(e.tools))
	for i, t := range e.tools {
		out[i] = cardToolEntry{Index: t.Index, Name: t.Name, Input: t.Input}
	}
	return out
}

func (e *turnStreamEmitter) emitMarkdownUpdate(ctx context.Context) {
	if e == nil || e.card == nil || e.card.Failed() {
		return
	}
	// Phase 1 dual-write: always push markdown so DingTalk/Slack/chat-api
	// parsers keep working. Phase 2 may skip this when structed != nil.
	_ = e.card.Update(ctx, buildCardContent(e.thinking, e.cardTools(), e.answer.String()))
}

func (e *turnStreamEmitter) emitEvent(ctx context.Context, ev TurnStreamEvent) {
	if e == nil || e.structed == nil {
		return
	}
	_ = e.structed.OnTurnStreamEvent(ctx, ev)
}

func (e *turnStreamEmitter) OnThinking(ctx context.Context, text string) {
	if e == nil {
		return
	}
	e.thinking = text
	e.emitEvent(ctx, TurnStreamEvent{Kind: TurnStreamThinkingReplace, Thinking: text})
	e.emitMarkdownUpdate(ctx)
}

func (e *turnStreamEmitter) OnToolUse(ctx context.Context, index int, name, input string) {
	if e == nil {
		return
	}
	tc := TurnToolCall{Index: index, Name: name, Input: input}
	e.tools = append(e.tools, tc)
	e.emitEvent(ctx, TurnStreamEvent{Kind: TurnStreamToolUpsert, Tool: tc})
	e.emitMarkdownUpdate(ctx)
}

func (e *turnStreamEmitter) OnToolResult(ctx context.Context, index int, name string, res TurnToolResult) {
	if e == nil {
		return
	}
	resCopy := res
	tc := TurnToolCall{Index: index, Name: name, Result: &resCopy}
	for i := range e.tools {
		if e.tools[i].Index == index {
			e.tools[i].Result = &resCopy
			if name == "" {
				name = e.tools[i].Name
			} else {
				e.tools[i].Name = name
			}
			tc.Name = e.tools[i].Name
			tc.Input = e.tools[i].Input
			break
		}
	}
	e.emitEvent(ctx, TurnStreamEvent{Kind: TurnStreamToolResult, Tool: tc})
	// Markdown layout does not include tool results today; still refresh so
	// dual-write stays consistent if buildCardContent gains result fields later.
	e.emitMarkdownUpdate(ctx)
}

// OnToolResultPending attaches a result to the first pending tool matching name
// (or the first pending tool if name does not match). Used when EventToolResult
// does not carry a stable index.
func (e *turnStreamEmitter) OnToolResultPending(ctx context.Context, name string, res TurnToolResult) {
	if e == nil {
		return
	}
	match := -1
	fallback := -1
	for i := range e.tools {
		if e.tools[i].Result != nil {
			continue
		}
		if fallback < 0 {
			fallback = i
		}
		if name != "" && e.tools[i].Name == name {
			match = i
			break
		}
	}
	if match < 0 {
		match = fallback
	}
	if match < 0 {
		e.OnToolResult(ctx, 0, name, res)
		return
	}
	e.OnToolResult(ctx, e.tools[match].Index, name, res)
}

// OnAnswerText sets the full answer so far and emits Append or Replace.
func (e *turnStreamEmitter) OnAnswerText(ctx context.Context, full string) {
	if e == nil {
		return
	}
	prev := e.answer.String()
	if full == prev {
		return
	}
	e.answer.Reset()
	e.answer.WriteString(full)
	if e.structed != nil {
		if strings.HasPrefix(full, prev) {
			suffix := full[len(prev):]
			if suffix != "" {
				e.emitEvent(ctx, TurnStreamEvent{Kind: TurnStreamAnswerAppend, Answer: suffix})
			}
		} else {
			e.emitEvent(ctx, TurnStreamEvent{Kind: TurnStreamAnswerReplace, Answer: full})
		}
	}
	e.emitMarkdownUpdate(ctx)
}

func (e *turnStreamEmitter) Finalize(ctx context.Context, answer string) error {
	if e == nil || e.card == nil {
		return nil
	}
	if answer != e.answer.String() {
		// Sync final answer without an extra mid-stream event when unchanged
		// path already streamed; emit replace/append only if different.
		e.OnAnswerText(ctx, answer)
	}
	content := buildCardContent(e.thinking, e.cardTools(), answer)
	return e.card.Finalize(ctx, content)
}
