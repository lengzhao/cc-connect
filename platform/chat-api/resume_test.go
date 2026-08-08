package chatapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestResumeReplaysLastTextDeltaAfterDisconnect(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		ssc := card.(core.StructuredStreamingCard)
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamAnswerAppend, Answer: "draft-1",
		})
		<-release
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamAnswerReplace, Answer: "final-after-disconnect",
		})
		time.Sleep(80 * time.Millisecond)
		_ = card.Finalize(context.Background(), "")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"hi"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	go func() {
		rec := httptest.NewRecorder()
		p.routes().ServeHTTP(rec, req)
	}()

	runID := waitRunID(t, p)
	time.Sleep(40 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)
	close(release)
	time.Sleep(40 * time.Millisecond)

	run := p.pending.get(runID)
	if run == nil {
		t.Fatal("run should remain after disconnect while turn active")
	}
	run.mu.Lock()
	ev := run.lastRecoverableEvent
	run.mu.Unlock()
	if ev == nil || ev.name != "text_delta" {
		t.Fatalf("expected cached text_delta, got %#v", ev)
	}
	payload, _ := ev.payload.(map[string]any)
	if payload["text"] != "final-after-disconnect" {
		t.Fatalf("cached text = %#v", payload)
	}
	if payload["replace"] != true {
		t.Fatalf("expected replace:true, got %#v", payload)
	}

	resumeReq := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"run_id":`+jsonQuote(runID)+`}`))
	resumeReq.Header.Set("Authorization", "Bearer secret")
	resumeReq.Header.Set("X-Chat-API-User", "user_001")
	resumeReq.Header.Set("X-Chat-API-Channel", testChannel)
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeReq.Header.Set("Accept", "text/event-stream")
	resumeRec := httptest.NewRecorder()
	p.routes().ServeHTTP(resumeRec, resumeReq)

	events := parseSSE(resumeRec.Body.String())
	if !hasEvent(events, "text_delta") {
		t.Fatalf("missing text_delta on resume: %#v body=%s", events, resumeRec.Body.String())
	}
	foundReplace := false
	for _, e := range events {
		if e.Name != "text_delta" {
			continue
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(e.Data), &m)
		if m["text"] == "final-after-disconnect" && m["replace"] == true {
			foundReplace = true
		}
	}
	if !foundReplace {
		t.Fatalf("resume text_delta missing replace snapshot: %s", resumeRec.Body.String())
	}
	if !hasEvent(events, "message_end") {
		t.Fatalf("missing message_end: %#v", events)
	}
}

func TestResumeReplaysLastQuestionRequest(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	gate := make(chan struct{})
	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		<-gate
		_ = platform.(core.InlineButtonSender).SendWithButtons(context.Background(), msg.ReplyCtx, "Pick env", [][]core.ButtonOption{
			{{Text: "Staging", Data: "askq:0:1"}, {Text: "Production", Data: "askq:0:2"}},
		})
		<-release
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"deploy"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	go func() {
		p.routes().ServeHTTP(httptest.NewRecorder(), req)
	}()

	runID := waitRunID(t, p)
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)
	close(gate)
	time.Sleep(50 * time.Millisecond)

	run := p.pending.get(runID)
	if run == nil {
		t.Fatal("run missing")
	}
	ev := run.peekLastRecoverable()
	if ev == nil || ev.name != "question_request" {
		t.Fatalf("want cached question_request, got %#v", ev)
	}

	resumeReq := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"run_id":`+jsonQuote(runID)+`}`))
	resumeReq.Header.Set("Authorization", "Bearer secret")
	resumeReq.Header.Set("X-Chat-API-User", "user_001")
	resumeReq.Header.Set("X-Chat-API-Channel", testChannel)
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeReq.Header.Set("Accept", "text/event-stream")
	resumeRec := httptest.NewRecorder()
	go func() {
		p.routes().ServeHTTP(resumeRec, resumeReq)
	}()
	time.Sleep(50 * time.Millisecond)
	if !hasEvent(parseSSE(resumeRec.Body.String()), "question_request") {
		t.Fatalf("missing question_request replay: %s", resumeRec.Body.String())
	}
	close(release)
}

func TestResumeOnlyKeepsLastRecoverableEvent(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		<-release
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		ssc := card.(core.StructuredStreamingCard)
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamAnswerAppend, Answer: "first",
		})
		time.Sleep(20 * time.Millisecond)
		_ = platform.(core.InlineButtonSender).SendWithButtons(context.Background(), msg.ReplyCtx, "Q1", [][]core.ButtonOption{
			{{Text: "A", Data: "askq:0:1"}},
		})
		time.Sleep(20 * time.Millisecond)
		_ = platform.(core.InlineButtonSender).SendWithButtons(context.Background(), msg.ReplyCtx, "Q2", [][]core.ButtonOption{
			{{Text: "B", Data: "askq:0:1"}},
		})
		time.Sleep(30 * time.Millisecond)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"x"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	go func() {
		p.routes().ServeHTTP(httptest.NewRecorder(), req)
	}()

	runID := waitRunID(t, p)
	cancel()
	time.Sleep(20 * time.Millisecond)
	close(release)
	time.Sleep(100 * time.Millisecond)

	run := p.pending.get(runID)
	if run == nil {
		t.Fatal("run missing")
	}
	run.mu.Lock()
	ev := run.lastRecoverableEvent
	run.mu.Unlock()
	if ev == nil || ev.name != "question_request" {
		t.Fatalf("want last question_request, got %#v", ev)
	}
	payload, _ := json.Marshal(ev.payload)
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatal(err)
	}
	if m["prompt"] != "Q2" {
		t.Fatalf("prompt=%#v, want Q2 (last question should win)", m["prompt"])
	}
}

func TestResumeReplaysPingWhenIdleAfterDisconnect(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	block := make(chan struct{})
	p.setHandler(func(_ core.Platform, _ *core.Message) {
		<-block
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"hi"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	go func() {
		p.routes().ServeHTTP(httptest.NewRecorder(), req)
	}()

	runID := waitRunID(t, p)
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)

	run := p.pending.get(runID)
	if run == nil {
		t.Fatal("run should remain after disconnect while turn active")
	}
	ev := run.peekLastRecoverable()
	if ev == nil || ev.name != "ping" {
		t.Fatalf("expected ping cached on disconnect, got %#v", ev)
	}

	resumeReq := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"run_id":`+jsonQuote(runID)+`}`))
	resumeReq.Header.Set("Authorization", "Bearer secret")
	resumeReq.Header.Set("X-Chat-API-User", "user_001")
	resumeReq.Header.Set("X-Chat-API-Channel", testChannel)
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeReq.Header.Set("Accept", "text/event-stream")
	resumeRec := httptest.NewRecorder()
	go func() {
		p.routes().ServeHTTP(resumeRec, resumeReq)
	}()
	time.Sleep(50 * time.Millisecond)

	events := parseSSE(resumeRec.Body.String())
	foundPing := false
	for _, e := range events {
		if e.Name != "ping" {
			continue
		}
		foundPing = true
		var payload map[string]any
		if err := json.Unmarshal([]byte(e.Data), &payload); err != nil {
			t.Fatalf("ping payload: %v", err)
		}
		if payload["run_id"] != runID {
			t.Fatalf("ping run_id=%#v want %q", payload["run_id"], runID)
		}
	}
	if !foundPing {
		t.Fatalf("missing ping on resume: %#v body=%s", events, resumeRec.Body.String())
	}
	close(block)
}

func TestResumeAfterFinishedReturnsMessageEnd(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		<-release
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		_ = card.Finalize(context.Background(), "done offline")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"hi"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	go func() {
		p.routes().ServeHTTP(httptest.NewRecorder(), req)
	}()

	runID := waitRunID(t, p)
	cancel()
	time.Sleep(20 * time.Millisecond)
	close(release)
	waitRunGone(t, p, runID)

	resumeReq := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"run_id":`+jsonQuote(runID)+`}`))
	resumeReq.Header.Set("Authorization", "Bearer secret")
	resumeReq.Header.Set("X-Chat-API-User", "user_001")
	resumeReq.Header.Set("X-Chat-API-Channel", testChannel)
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeReq.Header.Set("Accept", "text/event-stream")
	resumeRec := httptest.NewRecorder()
	p.routes().ServeHTTP(resumeRec, resumeReq)
	if resumeRec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200 after finished run deleted", resumeRec.Code, resumeRec.Body.String())
	}
	if !hasEvent(parseSSE(resumeRec.Body.String()), "message_end") {
		t.Fatalf("missing message_end: %s", resumeRec.Body.String())
	}
}

func TestResumeUnknownRunReturnsMessageEnd(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"run_id":"run_does_not_exist"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if !hasEvent(parseSSE(rec.Body.String()), "message_end") {
		t.Fatalf("missing message_end: %s", rec.Body.String())
	}
}

func TestResumeWhileAttachedReturnsConflict(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	block := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		<-block
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		_ = card.Finalize(context.Background(), "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"hi"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	go func() {
		p.routes().ServeHTTP(httptest.NewRecorder(), req)
	}()
	runID := waitRunID(t, p)

	resumeReq := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"run_id":`+jsonQuote(runID)+`}`))
	resumeReq.Header.Set("Authorization", "Bearer secret")
	resumeReq.Header.Set("X-Chat-API-User", "user_001")
	resumeReq.Header.Set("X-Chat-API-Channel", testChannel)
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeReq.Header.Set("Accept", "text/event-stream")
	resumeRec := httptest.NewRecorder()
	p.routes().ServeHTTP(resumeRec, resumeReq)
	if resumeRec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", resumeRec.Code, resumeRec.Body.String())
	}
	close(block)
}

func TestDetachedFinishDeletesImmediately(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	run := newRunState("run_done", "u", "ch", "sk", "c", "c:0", p, nil, time.Now().Add(time.Minute))
	if !p.pending.create(run) {
		t.Fatal("create")
	}
	run.detach()
	if !p.pending.finish(run.id, pendingResult{answer: "offline"}) {
		t.Fatal("finish")
	}
	if p.pending.get(run.id) != nil {
		t.Fatal("finish must delete run immediately (live or detached)")
	}
	select {
	case got := <-run.done:
		if got.answer != "offline" {
			t.Fatalf("done answer=%q", got.answer)
		}
	default:
		t.Fatal("finish must still write done so an attached resume loop can wake")
	}
}

func TestResumeWakesWhenFinishAfterAttach(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s", "request_timeout": "30s"})
	bindTestSessions(t, p)

	gate := make(chan struct{})
	finishGate := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		<-gate
		<-finishGate
		_ = card.Finalize(context.Background(), "done-after-resume")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"hi"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	go func() { p.routes().ServeHTTP(httptest.NewRecorder(), req) }()

	runID := waitRunID(t, p)
	cancel()
	time.Sleep(30 * time.Millisecond)
	close(gate)

	resumeReq := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"run_id":`+jsonQuote(runID)+`}`))
	resumeReq.Header.Set("Authorization", "Bearer secret")
	resumeReq.Header.Set("X-Chat-API-User", "user_001")
	resumeReq.Header.Set("X-Chat-API-Channel", testChannel)
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeReq.Header.Set("Accept", "text/event-stream")
	resumeRec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		p.routes().ServeHTTP(resumeRec, resumeReq)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	close(finishGate)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("resume SSE hung after detached finish; want message_end wake")
	}
	if !hasEvent(parseSSE(resumeRec.Body.String()), "message_end") {
		t.Fatalf("missing message_end: %s", resumeRec.Body.String())
	}
	if p.pending.get(runID) != nil {
		t.Fatal("run should be deleted after resume consumed terminal")
	}
}

func TestDetachSeedsPingWhenNoRecoverableCache(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	run := newRunState("run_ping", "u", "ch", "sk", "c", "c:0", p, nil, time.Now().Add(time.Minute))
	run.detach()
	ev := run.peekLastRecoverable()
	if ev == nil || ev.name != "ping" {
		t.Fatalf("expected ping cache on disconnect, got %#v", ev)
	}
	payload, _ := ev.payload.(map[string]any)
	if payload["run_id"] != "run_ping" {
		t.Fatalf("ping run_id=%#v", payload["run_id"])
	}
}

func TestDetachedQuestionNotOverwrittenByText(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	gate := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		<-gate
		_ = platform.(core.InlineButtonSender).SendWithButtons(context.Background(), msg.ReplyCtx, "Pick", [][]core.ButtonOption{
			{{Text: "A", Data: "askq:0:1"}},
		})
		time.Sleep(20 * time.Millisecond)
		scp := platform.(core.StreamingCardPlatform)
		card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		ssc := card.(core.StructuredStreamingCard)
		_ = ssc.OnTurnStreamEvent(context.Background(), core.TurnStreamEvent{
			Kind: core.TurnStreamAnswerAppend, Answer: "should-not-replace-question",
		})
		time.Sleep(30 * time.Millisecond)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"q"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	go func() { p.routes().ServeHTTP(httptest.NewRecorder(), req) }()

	runID := waitRunID(t, p)
	cancel()
	time.Sleep(20 * time.Millisecond)
	close(gate)
	time.Sleep(80 * time.Millisecond)

	run := p.pending.get(runID)
	if run == nil {
		t.Fatal("run missing")
	}
	ev := run.peekLastRecoverable()
	if ev == nil || ev.name != "question_request" {
		t.Fatalf("want cached question_request, got %#v", ev)
	}
}

func waitRunID(t *testing.T, p *Platform) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.pending.mu.Lock()
		for id := range p.pending.runs {
			p.pending.mu.Unlock()
			return id
		}
		p.pending.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected active run")
	return ""
}

func waitRunGone(t *testing.T, p *Platform, runID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.pending.get(runID) == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected run to be deleted after finish")
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
