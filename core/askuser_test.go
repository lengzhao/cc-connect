package core

import "testing"

func TestIsStructuredAsk(t *testing.T) {
	if IsStructuredAsk(Event{ToolName: "AskUserQuestion"}) {
		t.Fatal("empty Questions must not be structured ask")
	}
	if !IsStructuredAsk(Event{
		ToolName:  "anything",
		Questions: []UserQuestion{{Question: "Q?"}},
	}) {
		t.Fatal("Questions present must be structured ask")
	}
}

func TestIsMCPAskTool(t *testing.T) {
	if !IsMCPAskTool(ToolCCConnectAskUser) || !IsMCPAskTool(MCPQualifiedAskUserTool) {
		t.Fatal("expected MCP ask tool names")
	}
	if IsMCPAskTool("AskUserQuestion") {
		t.Fatal("native AskUserQuestion is not MCP ask")
	}
}

func TestNormalizeAskUserEvent(t *testing.T) {
	if got := NormalizeAskUserEvent("create_task"); got != AskEventCreateTask {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeAskUserEvent("  "); got != "" {
		t.Fatalf("blank got %q", got)
	}
	if got := NormalizeAskUserEvent("foo"); got != "" {
		t.Fatalf("unknown got %q", got)
	}
}

func TestAskUserHub_AskComplete(t *testing.T) {
	h := NewAskUserHub()
	var got Event
	h.Bind("s1", askEmitterFunc(func(e Event) error {
		got = e
		go func() {
			h.Complete(e.RequestID, map[int]string{0: "boc"}, map[int]string{0: "中行"})
		}()
		return nil
	}))
	res, err := h.Ask(t.Context(), "s1", UserQuestion{
		Question:         "Which bank?",
		AllowCustomInput: true,
		Event:            "connect_account",
		Options:          []UserQuestionOption{{Label: "中行", Value: "boc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ToolName != ToolCCConnectAskUser || len(got.Questions) != 1 {
		t.Fatalf("event=%+v", got)
	}
	if !got.Questions[0].AllowCustomInput || got.Questions[0].Event != "connect_account" {
		t.Fatalf("question=%+v", got.Questions[0])
	}
	if res.Answers[0] != "boc" || res.DisplayAnswers[0] != "中行" {
		t.Fatalf("result=%+v", res)
	}
}

func TestAskUserHub_NoEmitter(t *testing.T) {
	h := NewAskUserHub()
	_, err := h.Ask(t.Context(), "missing", UserQuestion{Question: "Q"})
	if err == nil {
		t.Fatal("expected error")
	}
}

type askEmitterFunc func(Event) error

func (f askEmitterFunc) EmitAskUser(e Event) error { return f(e) }
