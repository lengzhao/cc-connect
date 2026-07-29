package chatapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestStructuredStreamNoMarkdownUpdate verifies Phase 2 primary path: typed
// events drive SSE, and markdown Update (Engine dual-write) is ignored so a
// card with "---" cannot truncate the answer.
func TestStructuredStreamNoMarkdownUpdate(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	bindTestSessions(t, p)

	fullAnswer := "正在获取 ETH/USDT 实时行情。\n\n---\n\n## 策略\n表格\n\n---\n\n结尾问句"
	// Truncated markdown that the old LastIndex parser would keep as answer.
	badMarkdown := streamThinkingHeader + "x" + streamSectionBreak +
		"---\n\n" + fullAnswer

	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		card, err := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		if err != nil {
			t.Errorf("CreateStreamingCard: %v", err)
			close(done)
			return
		}
		ssc, ok := card.(core.StructuredStreamingCard)
		if !ok {
			t.Error("streamingCard must implement StructuredStreamingCard")
			close(done)
			return
		}
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamAnswerAppend, Answer: fullAnswer,
		})
		// Dual-write markdown that would truncate if parsed.
		_ = card.Update(context.Background(), badMarkdown)
		_ = card.Finalize(context.Background(), badMarkdown)
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"query":"ETH/USDT"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	joined := collectTextDeltas(rec.Body.String())
	if joined != fullAnswer {
		t.Fatalf("joined = %q, want full answer\nSSE=%s", joined, rec.Body.String())
	}
	if !strings.Contains(joined, "## 策略") {
		t.Fatalf("mid section lost: %q", joined)
	}
}

func TestStructuredStreamToolEventsSkipMarkdownSniff(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	bindTestSessions(t, p)

	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
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
		// Engine still Reply-s 🧾 markdown in Phase 1 dual-write — must not duplicate.
		_ = platform.Reply(context.Background(), msg.ReplyCtx,
			"🧾 Bash\n🟢 状态: ok\n🔢 退出码: 0\n```text\nWed\n```")
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamAnswerAppend, Answer: "现在是周三",
		})
		_ = card.Finalize(context.Background(), "")
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"query":"time"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	if strings.Count(out, "event: tool_result") != 1 {
		t.Fatalf("expected exactly one tool_result, got SSE:\n%s", out)
	}
	if strings.Count(out, "event: tool_call") != 1 {
		t.Fatalf("expected exactly one tool_call, got SSE:\n%s", out)
	}
	if got := collectTextDeltas(out); got != "现在是周三" {
		t.Fatalf("answer = %q", got)
	}
}

func TestStructuredStreamAnswerReplace(t *testing.T) {
	rec := httptest.NewRecorder()
	sse, err := newSSEWriter(rec)
	if err != nil {
		t.Fatal(err)
	}
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", "", &Platform{}, sse, time.Time{})
	card := &streamingCard{platform: &Platform{pending: newPendingStore(10)}, rc: &replyContext{runID: "run1"}}
	card.platform.pending.create(run)

	_ = card.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
		Kind: core.TurnStreamAnswerAppend, Answer: "进度句",
	})
	_ = run.flushDelta()
	_ = card.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
		Kind: core.TurnStreamAnswerReplace, Answer: "澄清终稿",
	})
	_ = run.flushDelta()

	body := rec.Body.String()
	if !strings.Contains(body, `"replace":true`) {
		t.Fatalf("expected replace: %s", body)
	}
	if got := collectTextDeltas(body); got != "澄清终稿" {
		t.Fatalf("joined = %q", got)
	}
}
