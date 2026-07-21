package core

import (
	"context"
	"strings"
	"testing"
)

type recordingStructuredCard struct {
	updates   []string
	finals    []string
	events    []TurnStreamEvent
	failed    bool
	updateErr error
}

func (c *recordingStructuredCard) Update(_ context.Context, content string) error {
	c.updates = append(c.updates, content)
	return c.updateErr
}

func (c *recordingStructuredCard) Finalize(_ context.Context, content string) error {
	c.finals = append(c.finals, content)
	return nil
}

func (c *recordingStructuredCard) Failed() bool { return c.failed }

func (c *recordingStructuredCard) OnTurnStreamEvent(_ context.Context, ev TurnStreamEvent) error {
	c.events = append(c.events, ev)
	return nil
}

type markdownOnlyCard struct {
	updates []string
}

func (c *markdownOnlyCard) Update(_ context.Context, content string) error {
	c.updates = append(c.updates, content)
	return nil
}
func (c *markdownOnlyCard) Finalize(_ context.Context, _ string) error { return nil }
func (c *markdownOnlyCard) Failed() bool                               { return false }

func TestTurnStreamEmitter_AnswerAppendAndReplace(t *testing.T) {
	card := &recordingStructuredCard{}
	em := newTurnStreamEmitter(card)

	em.OnAnswerText(context.Background(), "Hel")
	em.OnAnswerText(context.Background(), "Hello")
	em.OnAnswerText(context.Background(), "请提供交易对") // non-prefix → replace

	if len(card.events) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(card.events), card.events)
	}
	if card.events[0].Kind != TurnStreamAnswerAppend || card.events[0].Answer != "Hel" {
		t.Fatalf("event0 = %+v", card.events[0])
	}
	if card.events[1].Kind != TurnStreamAnswerAppend || card.events[1].Answer != "lo" {
		t.Fatalf("event1 = %+v", card.events[1])
	}
	if card.events[2].Kind != TurnStreamAnswerReplace || card.events[2].Answer != "请提供交易对" {
		t.Fatalf("event2 = %+v", card.events[2])
	}
	if got := em.Answer(); got != "请提供交易对" {
		t.Fatalf("answer = %q", got)
	}
	if len(card.updates) != 3 {
		t.Fatalf("updates = %d, want 3", len(card.updates))
	}
	if !strings.Contains(card.updates[1], "Hello") {
		t.Fatalf("update1 missing Hello: %q", card.updates[1])
	}
}

func TestTurnStreamEmitter_ToolUseAndResult(t *testing.T) {
	card := &recordingStructuredCard{}
	em := newTurnStreamEmitter(card)

	em.OnThinking(context.Background(), "plan")
	em.OnToolUse(context.Background(), 1, "Bash", "```bash\ndate\n```")
	ok := true
	code := 0
	em.OnToolResult(context.Background(), 1, "Bash", TurnToolResult{
		Output: "Wed", Status: "ok", ExitCode: &code, Success: &ok,
	})
	em.OnAnswerText(context.Background(), "done")

	kinds := make([]TurnStreamEventKind, len(card.events))
	for i, ev := range card.events {
		kinds[i] = ev.Kind
	}
	want := []TurnStreamEventKind{
		TurnStreamThinkingReplace,
		TurnStreamToolUpsert,
		TurnStreamToolResult,
		TurnStreamAnswerAppend,
	}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds[%d] = %v, want %v (all=%v)", i, kinds[i], want[i], kinds)
		}
	}
	if card.events[2].Tool.Result == nil || card.events[2].Tool.Result.Output != "Wed" {
		t.Fatalf("tool result = %+v", card.events[2].Tool)
	}
	snap := em.Snapshot()
	if snap.Thinking != "plan" || snap.Answer != "done" || len(snap.Tools) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Tools[0].Result == nil || snap.Tools[0].Result.Status != "ok" {
		t.Fatalf("tool snap = %+v", snap.Tools[0])
	}
}

func TestTurnStreamEmitter_MarkdownOnlyNoEvents(t *testing.T) {
	card := &markdownOnlyCard{}
	em := newTurnStreamEmitter(card)
	em.OnThinking(context.Background(), "t")
	em.OnAnswerText(context.Background(), "a")
	if len(card.updates) != 2 {
		t.Fatalf("updates = %d", len(card.updates))
	}
	wantThink := buildCardContent("t", nil, "")
	if card.updates[0] != wantThink {
		t.Fatalf("update0 = %q, want %q", card.updates[0], wantThink)
	}
}

func TestTurnStreamEmitter_FinalizeSyncsAnswer(t *testing.T) {
	card := &recordingStructuredCard{}
	em := newTurnStreamEmitter(card)
	em.OnAnswerText(context.Background(), "hel")
	if err := em.Finalize(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(card.finals) != 1 {
		t.Fatalf("finals = %d", len(card.finals))
	}
	want := buildCardContent("", nil, "hello")
	if card.finals[0] != want {
		t.Fatalf("final = %q, want %q", card.finals[0], want)
	}
	foundAppend := false
	for _, ev := range card.events {
		if ev.Kind == TurnStreamAnswerAppend && ev.Answer == "lo" {
			foundAppend = true
		}
	}
	if !foundAppend {
		t.Fatalf("expected append lo, events=%+v", card.events)
	}
}
