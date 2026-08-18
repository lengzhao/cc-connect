package chatapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func TestTruncateForLog(t *testing.T) {
	if got := truncateForLog("短文本", 40); got != "短文本" {
		t.Fatalf("short = %q", got)
	}
	long := strings.Repeat("测", 50)
	if got := truncateForLog(long, 40); got != strings.Repeat("测", 40) {
		t.Fatalf("truncated len=%d got=%q", utf8.RuneCountInString(got), got)
	}
}

func TestRunStateThinkingAndAnswerDeltas(t *testing.T) {
	rec := httptest.NewRecorder()
	sse, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("newSSEWriter: %v", err)
	}
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", &Platform{}, sse, time.Time{})

	run.setStreamContent("plan", "")
	if err := run.flushDelta(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "event: thinking_delta") {
		t.Fatalf("body = %s", rec.Body.String())
	}

	fullCard := streamThinkingHeader + "plan" + streamSectionBreak + streamSectionBreak + "hello"
	thinking, answer := parseStreamingCardContent(fullCard)
	run.setStreamContent(thinking, answer)
	rec.Body.Reset()
	if err := run.flushDelta(); err != nil {
		t.Fatalf("flush2: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: text_delta") {
		t.Fatalf("expected text_delta, body = %s", body)
	}
}

func TestEmitTerminalSSEFlushesPendingDelta(t *testing.T) {
	p := newTestPlatform(t, map[string]any{})
	rec := httptest.NewRecorder()
	sse, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("newSSEWriter: %v", err)
	}
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", &Platform{}, sse, time.Time{})
	run.setStreamContent("", "pending tail")
	// Do not flush manually — emitTerminalSSE must drain before message_end.
	p.emitTerminalSSE(run, pendingResult{answer: "pending tail"})
	body := rec.Body.String()
	if !strings.Contains(body, "event: text_delta") {
		t.Fatalf("expected flushed text_delta, body = %s", body)
	}
	if strings.Index(body, "event: text_delta") > strings.Index(body, "event: message_end") {
		t.Fatalf("text_delta should precede message_end, body = %s", body)
	}
}

func TestFinalizeSyncsStreamBeforeFinish(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	bindTestSessions(t, p)

	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp, ok := platform.(core.StreamingCardPlatform)
		if !ok {
			t.Error("not StreamingCardPlatform")
			close(done)
			return
		}
		card, err := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		if err != nil {
			t.Errorf("CreateStreamingCard: %v", err)
			close(done)
			return
		}
		partial := streamThinkingHeader + "think" + streamSectionBreak + streamSectionBreak + "hel"
		_ = card.Update(context.Background(), partial)
		_ = card.Finalize(context.Background(), partial+"lo")
		close(done)
	})

	body := `{"query":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	if !strings.Contains(out, "event: thinking_delta") {
		t.Fatalf("missing thinking_delta: %s", out)
	}
	if !strings.Contains(out, `event: text_delta`) || !strings.Contains(out, "hello") {
		t.Fatalf("missing final answer delta: %s", out)
	}
	lastDelta := strings.LastIndex(out, "event: text_delta")
	endIdx := strings.Index(out, "event: message_end")
	if lastDelta < 0 || endIdx < 0 || lastDelta > endIdx {
		t.Fatalf("text_delta should appear before message_end: %s", out)
	}
}

func TestReplyFinishesPlainReplyWithoutStreamingCard(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	bindTestSessions(t, p)
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		_ = platform.Reply(context.Background(), msg.ReplyCtx, "workspace init hint")
	})

	body := `{"query":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", "chat-123")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	out := rec.Body.String()
	if !strings.Contains(out, "event: text_delta") || !strings.Contains(out, "workspace init hint") {
		t.Fatalf("missing plain reply delta: %s", out)
	}
	if !strings.Contains(out, "event: message_end") {
		t.Fatalf("missing message_end: %s", out)
	}
	if strings.Contains(out, "request timed out") {
		t.Fatalf("plain reply should not time out: %s", out)
	}
}

func TestMessageEndOmitsAnswerByDefault(t *testing.T) {
	p := newTestPlatform(t, map[string]any{})
	rec := httptest.NewRecorder()
	sse, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("newSSEWriter: %v", err)
	}
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", &Platform{}, sse, time.Time{})
	run.setStreamContent("", "hello")
	p.emitTerminalSSE(run, pendingResult{answer: "hello"})
	if strings.Contains(rec.Body.String(), `"answer"`) {
		t.Fatalf("answer should be omitted, body = %s", rec.Body.String())
	}
}

func TestMessageEndIncludesAnswerWhenConfigured(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"include_answer_in_message_end": true})
	rec := httptest.NewRecorder()
	sse, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("newSSEWriter: %v", err)
	}
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", &Platform{}, sse, time.Time{})
	run.setStreamContent("", "hello")
	p.emitTerminalSSE(run, pendingResult{answer: "hello"})
	if !strings.Contains(rec.Body.String(), `"answer":"hello"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestToolCallAndResultSSENotInTextDelta(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
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
			Kind:     core.TurnStreamThinkingReplace,
			Thinking: "need clock",
		})
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamToolUpsert,
			Tool: core.TurnToolCall{Index: 1, Name: "Bash", Input: "date"},
		})
		ok := true
		code := 0
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamToolResult,
			Tool: core.TurnToolCall{Index: 1, Name: "Bash", Result: &core.TurnToolResult{
				Output: "2026年 7月15日", Status: "ok", ExitCode: &code, Success: &ok,
			}},
		})
		// Legacy 🧾 Reply must be dropped (Phase 3), not become text_delta.
		_ = platform.Reply(context.Background(), msg.ReplyCtx,
			"🧾 Bash\n🟢 状态: ok\n🔢 退出码: 0\n```text\n2026年 7月15日\n```")
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind:   core.TurnStreamAnswerAppend,
			Answer: "我来帮你查看当前时间。现在是 **2026年7月15日**",
		})
		_ = card.Finalize(context.Background(), "")
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"现在是什么时间"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	if !strings.Contains(out, "event: tool_call") {
		t.Fatalf("missing tool_call: %s", out)
	}
	if !strings.Contains(out, "event: tool_result") {
		t.Fatalf("missing tool_result: %s", out)
	}
	if !strings.Contains(out, `"name":"Bash"`) {
		t.Fatalf("expected Bash in tool events: %s", out)
	}
	if strings.Count(out, "event: tool_result") != 1 {
		t.Fatalf("expected one tool_result, got: %s", out)
	}
	for _, block := range strings.Split(out, "\n\n") {
		if !strings.Contains(block, "event: text_delta") {
			continue
		}
		if strings.Contains(block, "🧾") || strings.Contains(block, "状态:") || strings.Contains(block, "退出码") || strings.Contains(block, "Tool #") {
			t.Fatalf("tool markdown leaked into text_delta: %s", block)
		}
	}
	joined := collectTextDeltas(out)
	if strings.Count(joined, "现在是 **2026年7月15日**") != 1 {
		t.Fatalf("answer should appear once, got %q from %s", joined, out)
	}
	if !strings.Contains(joined, "我来帮你查看当前时间。现在是 **2026年7月15日**") {
		t.Fatalf("unexpected joined answer %q", joined)
	}
}

func collectTextDeltas(sseBody string) string {
	var b strings.Builder
	for _, block := range strings.Split(sseBody, "\n\n") {
		if !strings.Contains(block, "event: text_delta") {
			continue
		}
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var payload map[string]any
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				continue
			}
			text, _ := payload["text"].(string)
			if replace, _ := payload["replace"].(bool); replace {
				b.Reset()
			}
			b.WriteString(text)
		}
	}
	return b.String()
}

func TestAnswerDeltaReplaceOnNonPrefixChange(t *testing.T) {
	rec := httptest.NewRecorder()
	sse, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("newSSEWriter: %v", err)
	}
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", &Platform{}, sse, time.Time{})

	run.setStreamContent("", "正在获取 ETH/USDT 实时行情。")
	if err := run.flushDelta(); err != nil {
		t.Fatalf("flush1: %v", err)
	}
	run.setStreamContent("", "请提供交易对，我来拉取实时行情并生成策略参数。")
	if err := run.flushDelta(); err != nil {
		t.Fatalf("flush2: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"replace":true`) {
		t.Fatalf("expected replace frame, body = %s", body)
	}
	joined := collectTextDeltas(body)
	want := "请提供交易对，我来拉取实时行情并生成策略参数。"
	if joined != want {
		t.Fatalf("joined = %q, want %q\nbody=%s", joined, want, body)
	}
	if strings.Contains(joined, "正在获取") {
		t.Fatalf("progress line leaked into answer: %q", joined)
	}
}

// TestUnknownSlashForwardKeepsRunOpenForAgentStream is a regression test for the
// bug where chat-api finished the SSE run on the "forwarding to agent" hint before
// the async agent turn could stream its response.
func TestUnknownSlashForwardKeepsRunOpenForAgentStream(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)

	forwarding := fmt.Sprintf(core.NewI18n(core.LangEnglish).T(core.MsgUnknownCommand), "/skill-guide")
	agentAnswer := "skill guide response from agent"

	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		if err := platform.Send(context.Background(), msg.ReplyCtx, forwarding); err != nil {
			t.Errorf("Send forwarding: %v", err)
		}
		sess := sm.GetOrCreateActive(msg.SessionKey)
		if !sess.TryLock() {
			t.Error("TryLock failed")
			close(done)
			return
		}
		defer sess.Unlock()

		scp, ok := platform.(core.StreamingCardPlatform)
		if !ok {
			t.Error("not StreamingCardPlatform")
			close(done)
			return
		}
		card, err := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		if err != nil {
			t.Errorf("CreateStreamingCard: %v", err)
			close(done)
			return
		}
		if err := card.Finalize(context.Background(), agentAnswer); err != nil {
			t.Errorf("Finalize: %v", err)
		}
		close(done)
	})

	body := `{"query":"/skill-guide hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	if !strings.Contains(out, agentAnswer) {
		t.Fatalf("missing agent response: %s", out)
	}
	if !strings.Contains(out, "event: message_end") {
		t.Fatalf("missing message_end: %s", out)
	}
	agentIdx := strings.Index(out, agentAnswer)
	endIdx := strings.Index(out, "event: message_end")
	if agentIdx < 0 || endIdx < 0 || agentIdx > endIdx {
		t.Fatalf("expected agent answer before message_end, got agent=%d end=%d\n%s", agentIdx, endIdx, out)
	}
}
