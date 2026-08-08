package chatapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func writeTransformConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transforms.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadToolSSETransforms(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "Bash",
    "emit": "thinking",
    "suppress": true,
    "text": {
      "en": "Running...",
      "zh": "执行中..."
    }
  }]
}`)
	reg, err := loadToolSSETransforms(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rule, ok := reg.lookup("bash")
	if !ok || rule.Emit != toolSSEEmitThinking || !rule.Suppress {
		t.Fatalf("lookup = %#v, ok=%v", rule, ok)
	}
	if got := pickTransformText(rule.Text, "zh"); got != "执行中..." {
		t.Fatalf("zh text = %q", got)
	}
	if got := pickTransformText(rule.Text, "ja"); got != "Running..." {
		t.Fatalf("fallback en = %q", got)
	}
}

func TestLoadToolSSETransforms_InvalidClientFlow(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "X",
    "emit": "client_flow",
    "text": { "en": "x" }
  }]
}`)
	if _, err := loadToolSSETransforms(path); err == nil || !strings.Contains(err.Error(), "flow_type") {
		t.Fatalf("want flow_type error, got %v", err)
	}
}

func TestToolSSETransform_ThinkingSuppress(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "Bash",
    "emit": "thinking",
    "suppress": true,
    "text": {
      "en": "Running command...",
      "zh": "正在执行命令..."
    }
  }]
}`)
	p := newTestPlatform(t, map[string]any{
		"token":                    "secret",
		"tool_sse_transforms_file": path,
		"agent_context_headers": map[string]any{
			"language": "X-Language",
		},
	})
	bindTestSessions(t, p)

	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		card, err := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		if err != nil {
			t.Errorf("CreateStreamingCard: %v", err)
			close(done)
			return
		}
		ssc := card.(core.StructuredStreamingCard)
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamToolUpsert,
			Tool: core.TurnToolCall{Index: 1, Name: "Bash", Input: "date"},
		})
		ok := true
		code := 0
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamToolResult,
			Tool: core.TurnToolCall{Index: 1, Name: "Bash", Result: &core.TurnToolResult{
				Output: "Wed", Status: "ok", ExitCode: &code, Success: &ok,
			}},
		})
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamAnswerAppend, Answer: "done",
		})
		_ = card.Finalize(context.Background(), "")
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"query":"run date"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Language", "zh")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	if strings.Contains(out, "event: tool_call") || strings.Contains(out, "event: tool_result") {
		t.Fatalf("tool SSE should be suppressed: %s", out)
	}
	if !strings.Contains(out, "event: thinking_delta") || !strings.Contains(out, "正在执行命令") {
		t.Fatalf("missing thinking transform: %s", out)
	}
}

func TestToolSSETransform_ClientFlow(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "CreateTask",
    "emit": "client_flow",
    "flow_type": "task_generating",
    "suppress": true,
    "text": {
      "en": "Generating task...",
      "zh": "正在生成任务..."
    }
  }]
}`)
	p := newTestPlatform(t, map[string]any{
		"token":                    "secret",
		"tool_sse_transforms_file": path,
	})
	bindTestSessions(t, p)

	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		ssc := card.(core.StructuredStreamingCard)
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamToolUpsert,
			Tool: core.TurnToolCall{Index: 1, Name: "CreateTask", Input: `{}`},
		})
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamAnswerAppend, Answer: "ok",
		})
		_ = card.Finalize(context.Background(), "")
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"query":"create"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	if !strings.Contains(out, "event: client_flow") {
		t.Fatalf("missing client_flow: %s", out)
	}
	if !strings.Contains(out, `"type":"task_generating"`) || !strings.Contains(out, "Generating task") {
		t.Fatalf("client_flow payload wrong: %s", out)
	}
	if strings.Contains(out, "event: tool_call") {
		t.Fatalf("tool_call should be suppressed: %s", out)
	}
}

func TestExtractJSONPathArg(t *testing.T) {
	cases := []struct {
		name   string
		output string
		path   string
		want   string
		ok     bool
	}{
		{name: "dollar root", output: `{"task_id":"t1"}`, path: "$.task_id", want: "t1", ok: true},
		{name: "nested", output: `{"data":{"task_id":"t2"}}`, path: "$.data.task_id", want: "t2", ok: true},
		{name: "no dollar", output: `{"data":{"task_id":"t3"}}`, path: "data.task_id", want: "t3", ok: true},
		{name: "number", output: `{"n":42}`, path: "$.n", want: "42", ok: true},
		{name: "bool", output: `{"ok":true}`, path: "$.ok", want: "true", ok: true},
		{name: "missing", output: `{"task_id":"t1"}`, path: "$.other", ok: false},
		{name: "object leaf", output: `{"data":{"id":"x"}}`, path: "$.data", ok: false},
		{name: "invalid json", output: `not-json`, path: "$.task_id", ok: false},
		{name: "empty path", output: `{"task_id":"t1"}`, path: "", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractJSONPathArg(tc.output, tc.path)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("extractJSONPathArg(%q, %q) = (%q, %v), want (%q, %v)",
					tc.output, tc.path, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestLoadToolSSETransforms_ArgsFromOnThinking(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "Bash",
    "emit": "thinking",
    "args_from": "$.task_id",
    "text": { "en": "x" }
  }]
}`)
	if _, err := loadToolSSETransforms(path); err == nil || !strings.Contains(err.Error(), "args_from") {
		t.Fatalf("want args_from error, got %v", err)
	}
}

func TestLoadToolSSETransforms_InvalidWhen(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "Bash",
    "emit": "thinking",
    "when": "both",
    "text": { "en": "x" }
  }]
}`)
	if _, err := loadToolSSETransforms(path); err == nil || !strings.Contains(err.Error(), "when") {
		t.Fatalf("want when error, got %v", err)
	}
}

func TestLoadToolSSETransforms_DefaultWhenIsToolCall(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "CreateTask",
    "emit": "client_flow",
    "flow_type": "task_generating",
    "args_from": "$.task_id",
    "text": { "en": "Generating..." }
  }]
}`)
	reg, err := loadToolSSETransforms(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rule, ok := reg.lookup("CreateTask")
	if !ok || rule.When != toolSSEWhenCall {
		t.Fatalf("default when = %#v ok=%v", rule, ok)
	}
	if rule.ArgsFrom != "$.task_id" {
		t.Fatalf("args_from = %q", rule.ArgsFrom)
	}
}

func TestToolSSETransform_ClientFlowArgsFromCall(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "Agent",
    "emit": "client_flow",
    "flow_type": "agent_call",
    "args_from": "$.message_id",
    "suppress": true,
    "text": { "en": "SubAgent running..." }
  }]
}`)
	p := newTestPlatform(t, map[string]any{
		"token":                    "secret",
		"tool_sse_transforms_file": path,
	})
	bindTestSessions(t, p)

	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		ssc := card.(core.StructuredStreamingCard)
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamToolUpsert,
			Tool: core.TurnToolCall{Index: 1, Name: "Agent", Input: `{"message_id":"msg_call_1"}`},
		})
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamAnswerAppend, Answer: "ok",
		})
		_ = card.Finalize(context.Background(), "")
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"query":"agent"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	if !strings.Contains(out, "event: client_flow") {
		t.Fatalf("missing client_flow: %s", out)
	}
	if !strings.Contains(out, `"args":"msg_call_1"`) {
		t.Fatalf("expected args from tool call input: %s", out)
	}
	if strings.Contains(out, "event: tool_call") {
		t.Fatalf("tool_call should be suppressed: %s", out)
	}
}

func TestToolSSETransform_ClientFlowArgsFromResult(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "CreateTask",
    "emit": "client_flow",
    "when": "tool_result",
    "flow_type": "task_generating",
    "args_from": "$.task_id",
    "suppress": true,
    "text": {
      "en": "Generating task...",
      "zh": "正在生成任务..."
    }
  }]
}`)
	p := newTestPlatform(t, map[string]any{
		"token":                    "secret",
		"tool_sse_transforms_file": path,
	})
	bindTestSessions(t, p)

	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		ssc := card.(core.StructuredStreamingCard)
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamToolUpsert,
			Tool: core.TurnToolCall{Index: 1, Name: "CreateTask", Input: `{"task_id":"from_call"}`},
		})
		ok := true
		code := 0
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamToolResult,
			Tool: core.TurnToolCall{Index: 1, Name: "CreateTask", Result: &core.TurnToolResult{
				Output: `{"task_id":"task_abc"}`, Status: "ok", ExitCode: &code, Success: &ok,
			}},
		})
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamAnswerAppend, Answer: "ok",
		})
		_ = card.Finalize(context.Background(), "")
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"query":"create"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	if !strings.Contains(out, "event: client_flow") {
		t.Fatalf("missing client_flow: %s", out)
	}
	if !strings.Contains(out, `"type":"task_generating"`) || !strings.Contains(out, "Generating task") {
		t.Fatalf("client_flow payload wrong: %s", out)
	}
	if !strings.Contains(out, `"args":"task_abc"`) {
		t.Fatalf("expected args from result: %s", out)
	}
	if strings.Contains(out, `"args":"from_call"`) {
		t.Fatalf("should not use call input when when=tool_result: %s", out)
	}
	if strings.Contains(out, "event: tool_call") || strings.Contains(out, "event: tool_result") {
		t.Fatalf("tool SSE should be suppressed: %s", out)
	}
	if n := strings.Count(out, "event: client_flow"); n != 1 {
		t.Fatalf("want exactly one client_flow, got %d: %s", n, out)
	}
}

func TestToolSSETransform_ClientFlowArgsFromResultMissing(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "CreateTask",
    "emit": "client_flow",
    "when": "tool_result",
    "flow_type": "task_generating",
    "args_from_result": "$.task_id",
    "suppress": true,
    "text": { "en": "Generating task..." }
  }]
}`)
	p := newTestPlatform(t, map[string]any{
		"token":                    "secret",
		"tool_sse_transforms_file": path,
	})
	bindTestSessions(t, p)

	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		ssc := card.(core.StructuredStreamingCard)
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamToolUpsert,
			Tool: core.TurnToolCall{Index: 1, Name: "CreateTask", Input: `{}`},
		})
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamToolResult,
			Tool: core.TurnToolCall{Index: 1, Name: "CreateTask", Result: &core.TurnToolResult{
				Output: `{"other":"x"}`, Status: "ok",
			}},
		})
		_ = card.Finalize(context.Background(), "")
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"query":"create"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	if !strings.Contains(out, "event: client_flow") {
		t.Fatalf("missing client_flow: %s", out)
	}
	if strings.Contains(out, `"args"`) {
		t.Fatalf("args should be omitted when path missing: %s", out)
	}
}

func TestToolSSETransform_ThinkingWhenToolResult(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "Bash",
    "emit": "thinking",
    "when": "tool_result",
    "suppress": true,
    "text": { "en": "Command finished" }
  }]
}`)
	p := newTestPlatform(t, map[string]any{
		"token":                    "secret",
		"tool_sse_transforms_file": path,
	})
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", "en", p, nil, time.Now().Add(time.Minute))
	run.upsertStructuredTool("1", "Bash", "date")
	rec := httptest.NewRecorder()
	sse, err := newSSEWriter(rec)
	if err != nil {
		t.Fatal(err)
	}
	run.sink = &sseEventSink{w: sse}
	if err := run.flushDelta(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "Command finished") {
		t.Fatalf("thinking should wait for tool_result: %s", rec.Body.String())
	}
	run.enqueueStructuredToolResult(streamToolResult{Name: "Bash", Output: "Wed", Status: "ok"})
	rec.Body.Reset()
	if err := run.flushDelta(); err != nil {
		t.Fatal(err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: thinking_delta") || !strings.Contains(out, "Command finished") {
		t.Fatalf("missing deferred thinking: %s", out)
	}
}

func TestToolSSETransform_UnmatchedPassthrough(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "Bash",
    "emit": "thinking",
    "suppress": true,
    "text": { "en": "Running..." }
  }]
}`)
	p := newTestPlatform(t, map[string]any{
		"token":                    "secret",
		"tool_sse_transforms_file": path,
	})
	bindTestSessions(t, p)

	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		ssc := card.(core.StructuredStreamingCard)
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamToolUpsert,
			Tool: core.TurnToolCall{Index: 1, Name: "Read", Input: "a.go"},
		})
		_ = card.Finalize(context.Background(), "")
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"query":"read"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	if !strings.Contains(out, "event: tool_call") || !strings.Contains(out, `"name":"Read"`) {
		t.Fatalf("unmatched tool should pass through: %s", out)
	}
}

func TestResolveRunLanguage(t *testing.T) {
	if got := resolveRunLanguage("zh-CN"); got != "zh" {
		t.Fatalf("got %q", got)
	}
	if got := resolveRunLanguage(""); got != "en" {
		t.Fatalf("empty -> en, got %q", got)
	}
}

func TestPickTransformText_FallbackFirst(t *testing.T) {
	text := map[string]string{"de": "Hallo"}
	if got := pickTransformText(text, "de"); got != "Hallo" {
		t.Fatalf("got %q", got)
	}
}

func TestNewToolSSETransformsFileMissing(t *testing.T) {
	_, err := New(map[string]any{"tool_sse_transforms_file": filepath.Join(t.TempDir(), "missing.json")})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestToolSSETransform_NoSuppressStillEmitsToolCall(t *testing.T) {
	path := writeTransformConfig(t, `{
  "transforms": [{
    "tool": "Bash",
    "emit": "thinking",
    "suppress": false,
    "text": { "en": "Running..." }
  }]
}`)
	p := newTestPlatform(t, map[string]any{
		"token":                    "secret",
		"tool_sse_transforms_file": path,
	})
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", "en", p, nil, time.Now().Add(time.Minute))
	run.upsertStructuredTool("1", "Bash", "date")
	rec := httptest.NewRecorder()
	sse, err := newSSEWriter(rec)
	if err != nil {
		t.Fatal(err)
	}
	run.sink = &sseEventSink{w: sse}
	if err := run.flushDelta(); err != nil {
		t.Fatal(err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: thinking_delta") || !strings.Contains(out, "Running...") {
		t.Fatalf("missing thinking: %s", out)
	}
	if !strings.Contains(out, "event: tool_call") {
		t.Fatalf("tool_call should remain when suppress=false: %s", out)
	}
}

func TestLoadToolSSETransforms_InvalidJSON(t *testing.T) {
	path := writeTransformConfig(t, `{not json`)
	if _, err := loadToolSSETransforms(path); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadToolSSETransforms_DefaultOnly(t *testing.T) {
	path := writeTransformConfig(t, `{
  "default": {
    "emit": "thinking",
    "suppress": true,
    "text": { "en": "Running {tool}..." }
  }
}`)
	reg, err := loadToolSSETransforms(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rule, ok := reg.lookup("Read")
	if !ok || rule.Emit != toolSSEEmitThinking {
		t.Fatalf("default lookup = %#v ok=%v", rule, ok)
	}
}

func TestFormatTransformText_ToolPlaceholder(t *testing.T) {
	text := map[string]string{"zh": "正在执行 {tool}..."}
	if got := formatTransformText(text, "zh", "Read"); got != "正在执行 Read..." {
		t.Fatalf("got %q", got)
	}
}

func TestToolSSETransform_DefaultRule(t *testing.T) {
	path := writeTransformConfig(t, `{
  "default": {
    "emit": "thinking",
    "suppress": true,
    "text": {
      "en": "Running {tool}...",
      "zh": "正在执行 {tool}..."
    }
  },
  "transforms": [{
    "tool": "Bash",
    "emit": "thinking",
    "suppress": true,
    "text": { "zh": "正在执行命令..." }
  }]
}`)
	p := newTestPlatform(t, map[string]any{
		"token":                    "secret",
		"tool_sse_transforms_file": path,
		"agent_context_headers": map[string]any{
			"language": "X-Language",
		},
	})
	bindTestSessions(t, p)

	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		ssc := card.(core.StructuredStreamingCard)
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamToolUpsert,
			Tool: core.TurnToolCall{Index: 1, Name: "Read", Input: "a.go"},
		})
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamAnswerAppend, Answer: "ok",
		})
		_ = card.Finalize(context.Background(), "")
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"query":"read"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Language", "zh")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	if strings.Contains(out, "event: tool_call") {
		t.Fatalf("default suppress should hide tool_call: %s", out)
	}
	if !strings.Contains(out, "正在执行 Read") {
		t.Fatalf("default text with {tool} missing: %s", out)
	}
}

func TestToolSSETransform_SpecificOverridesDefault(t *testing.T) {
	path := writeTransformConfig(t, `{
  "default": {
    "emit": "thinking",
    "suppress": true,
    "text": { "en": "Default {tool}" }
  },
  "transforms": [{
    "tool": "Bash",
    "emit": "thinking",
    "suppress": true,
    "text": { "en": "Custom bash" }
  }]
}`)
	p := newTestPlatform(t, map[string]any{
		"token":                    "secret",
		"tool_sse_transforms_file": path,
	})
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", "en", p, nil, time.Now().Add(time.Minute))
	run.upsertStructuredTool("1", "Bash", "date")
	run.upsertStructuredTool("2", "Read", "a.go")

	reg := p.toolSSETransforms
	bashRule, ok := reg.lookup("Bash")
	if !ok {
		t.Fatal("bash rule missing")
	}
	if text := formatTransformText(bashRule.Text, "en", "Bash"); text != "Custom bash" {
		t.Fatalf("bash = %q", text)
	}
	readRule, ok := reg.lookup("Read")
	if !ok {
		t.Fatal("read default missing")
	}
	if text := formatTransformText(readRule.Text, "en", "Read"); text != "Default Read" {
		t.Fatalf("read = %q", text)
	}
}
