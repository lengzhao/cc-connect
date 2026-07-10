package chatapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestRunStateThinkingAndAnswerDeltas(t *testing.T) {
	rec := httptest.NewRecorder()
	sse, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("newSSEWriter: %v", err)
	}
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", sse)

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
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", sse)
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
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", sse)
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
	run := newRunState("run1", "u", "", "sk", "s1", "s1:0", sse)
	run.setStreamContent("", "hello")
	p.emitTerminalSSE(run, pendingResult{answer: "hello"})
	if !strings.Contains(rec.Body.String(), `"answer":"hello"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
