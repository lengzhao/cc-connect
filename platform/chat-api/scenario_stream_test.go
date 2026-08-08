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

// TestScenarioClarificationReplacesProgress reproduces the "progress line then
// clarification" turn: a non-prefix answer rewrite must emit replace=true so an
// appending client ends with the clarification once (no progress leak).
func TestScenarioClarificationReplacesProgress(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	bindTestSessions(t, p)

	progress := "让我先查找 USDT 相关交易对信息。"
	final := "请提供交易对，我来拉取实时行情并生成策略参数。"

	updated := make(chan struct{})
	allowFinalize := make(chan struct{})
	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		card, err := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		if err != nil {
			t.Errorf("CreateStreamingCard: %v", err)
			close(done)
			return
		}
		_ = card.Update(context.Background(), progress)
		close(updated)
		<-allowFinalize
		_ = card.Finalize(context.Background(), final)
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"query":"帮我生成 USDT 的网格策略"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	httpDone := make(chan struct{})
	go func() {
		p.routes().ServeHTTP(rec, req)
		close(httpDone)
	}()

	<-updated
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(rec.Body.String(), progress) {
		if time.Now().After(deadline) {
			t.Fatalf("progress never flushed: %s", rec.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(allowFinalize)
	<-done
	<-httpDone

	out := rec.Body.String()
	if !strings.Contains(out, `"replace":true`) {
		t.Fatalf("expected replace frame: %s", out)
	}
	joined := collectTextDeltas(out)
	if joined != final {
		t.Fatalf("joined = %q, want %q", joined, final)
	}
	if strings.Contains(joined, "让我先查找") {
		t.Fatalf("progress leaked: %q", joined)
	}
}

// TestScenarioGridStrategyPreservesDividers reproduces the ETH/USDT strategy
// answer that contains multiple markdown "---" rules. SSE text_delta must keep
// the full answer (not truncate at LastIndex of the separator).
func TestScenarioGridStrategyPreservesDividers(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	bindTestSessions(t, p)

	answer := "正在获取 ETH/USDT 实时行情。\n\n---\n\n" +
		"## 网格策略\n| 档位 | 价格 |\n| --- | --- |\n| 1 | 100 |\n\n---\n\n" +
		"## 执行建议\n分批挂单\n\n---\n\n" +
		"## 风险提示\n注意回撤\n\n" +
		"需要我进一步细化某个版本，或帮你生成具体的挂单价格列表吗？"
	card := streamThinkingHeader + "fetch eth" + streamSectionBreak +
		"🔧 **Tool #1**: `Bash`\n```bash\ncurl quote\n```\n\n" +
		"---\n\n" + answer

	done := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		c, err := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		if err != nil {
			t.Errorf("CreateStreamingCard: %v", err)
			close(done)
			return
		}
		_ = c.Update(context.Background(), streamThinkingHeader+"fetch eth"+streamSectionBreak+"正在获取 ETH/USDT 实时行情。")
		_ = c.Finalize(context.Background(), card)
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"query":"ETH/USDT"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	joined := collectTextDeltas(out)
	if strings.Count(joined, "\n---\n") < 3 {
		t.Fatalf("expected >=3 markdown dividers preserved, got %q from %s", joined, out)
	}
	for _, part := range []string{"正在获取 ETH/USDT", "## 网格策略", "## 执行建议", "## 风险提示", "挂单价格列表"} {
		if !strings.Contains(joined, part) {
			t.Fatalf("missing %q in joined answer %q\nSSE=%s", part, joined, out)
		}
	}
	if strings.Contains(joined, "Tool #") || strings.Contains(joined, "curl quote") {
		t.Fatalf("tool markdown leaked into answer: %q", joined)
	}
	if strings.Count(joined, "挂单价格列表") != 1 {
		t.Fatalf("ending duplicated: %q", joined)
	}
}

// TestScenarioIncrementalAnswerIsAppendOnly verifies the common path: answer
// grows as a prefix, so only suffix frames are emitted (no replace).
func TestScenarioIncrementalAnswerIsAppendOnly(t *testing.T) {
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
		_ = card.Update(context.Background(), "Hel")
		_ = card.Update(context.Background(), "Hello")
		_ = card.Finalize(context.Background(), "Hello world")
		close(done)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"query":"hi"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	<-done

	out := rec.Body.String()
	if strings.Contains(out, `"replace":true`) {
		t.Fatalf("incremental path must not emit replace: %s", out)
	}
	if got := collectTextDeltas(out); got != "Hello world" {
		t.Fatalf("joined = %q, want Hello world\nSSE=%s", got, out)
	}
}
