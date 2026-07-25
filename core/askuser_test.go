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
	if got := NormalizeAskUserEvent("task_generating"); got != AskEventTaskGenerating {
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

func TestIsMCPClientFlowTool(t *testing.T) {
	if !IsMCPClientFlowTool(ToolCCConnectClientFlow) || !IsMCPClientFlowTool(MCPQualifiedClientFlowTool) {
		t.Fatal("expected client flow tool names")
	}
	if IsMCPClientFlowTool(ToolCCConnectAskUser) {
		t.Fatal("ask user is not client flow")
	}
	if IsMCPClientFlowTool("AskUserQuestion") {
		t.Fatal("native AskUserQuestion is not client flow")
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
	if got.ToolInputRaw["description"] != "绑定新账户" {
		t.Fatalf("raw description=%v", got.ToolInputRaw["description"])
	}
	if got.ToolInput != "绑定新账户" {
		t.Fatalf("description=%q", got.ToolInput)
	}
	if got.RequestID == "" {
		t.Fatal("expected RequestID for tracing")
	}
}

func TestAskUserHub_EmitClientFlow_InvalidType(t *testing.T) {
	h := NewAskUserHub()
	h.Bind("s1", askEmitterFunc(func(e Event) error { return nil }))
	if err := h.EmitClientFlow("s1", "account_bind", "x"); err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestAskUserHub_EmitClientFlow_EmptyDescription(t *testing.T) {
	h := NewAskUserHub()
	h.Bind("s1", askEmitterFunc(func(e Event) error { return nil }))
	if err := h.EmitClientFlow("s1", "connect_account", "  "); err == nil {
		t.Fatal("expected error for empty description")
	}
}

func TestAskUserHub_EmitClientFlow_NoEmitter(t *testing.T) {
	h := NewAskUserHub()
	if err := h.EmitClientFlow("missing", "connect_account", "x"); err == nil {
		t.Fatal("expected error when no emitter")
	}
}

type askEmitterFunc func(Event) error

func (f askEmitterFunc) EmitAskUser(e Event) error { return f(e) }
