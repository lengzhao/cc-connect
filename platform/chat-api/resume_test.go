package chatapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestResumeReplaysLastTextDeltaAfterDisconnect(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	release := make(chan struct{})
	var replyCtx any
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		replyCtx = msg.ReplyCtx
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
		// Keep turn open until resume has attached and observed the cached event.
		time.Sleep(80 * time.Millisecond)
		_ = card.Finalize(context.Background(), "")
	})

	body := `{"query":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
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
	cancel() // disconnect
	time.Sleep(30 * time.Millisecond)
	close(release)
	time.Sleep(40 * time.Millisecond)

	if replyCtx == nil {
		t.Fatal("expected reply context")
	}
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

	resumeBody := `{"run_id":` + jsonQuote(runID) + `}`
	resumeReq := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(resumeBody))
	resumeReq.Header.Set("Authorization", "Bearer secret")
	resumeReq.Header.Set("X-Chat-API-User", "user_001")
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

func TestResumeReplaysLastQuestionAndNotifies(t *testing.T) {
	var notifyHits atomic.Int32
	var notifyBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		notifyHits.Add(1)
		b, _ := io.ReadAll(r.Body)
		notifyBody.Store(string(b))
		if r.Header.Get("X-Chat-API-Notify-Secret") != "sec" {
			t.Errorf("missing notify secret header")
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	p := newTestPlatform(t, map[string]any{
		"token":                   "secret",
		"sse_ping_interval":       "0s",
		"question_notify_url":     srv.URL,
		"question_notify_secret":  "sec",
		"question_notify_timeout": "2s",
	})
	bindTestSessions(t, p)

	gate := make(chan struct{})
	questionReady := make(chan string, 1)
	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		if msg.Content == "askq:0:1" || strings.HasPrefix(msg.Content, "askq:") {
			close(release)
			if scp, ok := platform.(core.StreamingCardPlatform); ok {
				c, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
				_ = c.Finalize(context.Background(), "done")
			}
			return
		}
		<-gate // wait until client disconnected
		_ = platform.(core.AskQuestionSender).SendAskQuestion(context.Background(), msg.ReplyCtx, core.UserQuestion{
			Question: "Pick env",
			Options: []core.UserQuestionOption{
				{Label: "Staging", Value: "Staging"},
				{Label: "Production", Value: "Production"},
			},
		}, 0)
		rc := msg.ReplyCtx.(*replyContext)
		questionReady <- rc.runID
		<-release
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"deploy"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	go func() {
		rec := httptest.NewRecorder()
		p.routes().ServeHTTP(rec, req)
	}()

	runID := waitRunID(t, p)
	time.Sleep(30 * time.Millisecond)
	cancel() // disconnect before question
	time.Sleep(30 * time.Millisecond)
	close(gate)

	select {
	case <-questionReady:
	case <-time.After(2 * time.Second):
		t.Fatal("question not emitted")
	}
	deadline := time.Now().Add(2 * time.Second)
	for notifyHits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if notifyHits.Load() == 0 {
		t.Fatal("expected question_notify webhook")
	}
	run := p.pending.get(runID)
	if run == nil {
		t.Fatal("run missing")
	}
	raw, _ := notifyBody.Load().(string)
	var got map[string]string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("notify body = %s err=%v", raw, err)
	}
	want := map[string]string{
		"conversation_id": run.conversationID,
		"message_id":      run.messageID,
		"run_id":          runID,
		"user_id":         "user_001",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("notify body[%q]=%q want %q body=%s", k, got[k], v, raw)
		}
	}
	if _, ok := got["payload"]; ok {
		t.Fatalf("notify body must not include payload: %s", raw)
	}
	if _, ok := got["resume"]; ok {
		t.Fatalf("notify body must not include resume: %s", raw)
	}

	run.mu.Lock()
	ev := run.lastRecoverableEvent
	run.mu.Unlock()
	if ev == nil || ev.name != "question_request" {
		t.Fatalf("expected cached question_request, got %#v", ev)
	}

	resumeReq := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"run_id":`+jsonQuote(runID)+`}`))
	resumeReq.Header.Set("Authorization", "Bearer secret")
	resumeReq.Header.Set("X-Chat-API-User", "user_001")
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeReq.Header.Set("Accept", "text/event-stream")
	resumeRec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		p.routes().ServeHTTP(resumeRec, resumeReq)
		close(done)
	}()

	ixID := waitInteractionID(t, resumeRec, "question_request")
	respondRec := postRespond(t, p, `{"run_id":`+jsonQuote(runID)+`,"interaction_id":`+jsonQuote(ixID)+`,"answers":[{"index":0,"value":"Staging"}]}`)
	if respondRec.Code != http.StatusOK {
		t.Fatalf("respond status=%d body=%s", respondRec.Code, respondRec.Body.String())
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("resume SSE did not finish")
	}
	if !hasEvent(parseSSE(resumeRec.Body.String()), "message_end") {
		t.Fatalf("missing message_end: %s", resumeRec.Body.String())
	}
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
		_ = platform.(core.AskQuestionSender).SendAskQuestion(context.Background(), msg.ReplyCtx, core.UserQuestion{
			Question: "Q1",
			Options:  []core.UserQuestionOption{{Label: "A", Value: "A"}},
		}, 0)
		time.Sleep(20 * time.Millisecond)
		_ = platform.(core.AskQuestionSender).SendAskQuestion(context.Background(), msg.ReplyCtx, core.UserQuestion{
			Question: "Q2",
			Options:  []core.UserQuestionOption{{Label: "B", Value: "B"}},
		}, 0)
		time.Sleep(30 * time.Millisecond)
		// leave interaction open; test only checks cache
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"x"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
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
	cg, _ := m["card_group"].([]any)
	if len(cg) != 1 {
		t.Fatalf("card_group=%#v", cg)
	}
	card, _ := cg[0].(map[string]any)
	if card["title"] != "Q2" {
		t.Fatalf("title=%#v, want Q2 (last question should win)", card["title"])
	}
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

func TestResumeForeignRunReturnsMessageEnd(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	run := newRunState("run_foreign", "other_user", "ch", "sk", "c", "c:0", p, nil, time.Now().Add(time.Minute))
	if !p.pending.create(run) {
		t.Fatal("create")
	}
	run.detach()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"run_id":"run_foreign"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
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
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeReq.Header.Set("Accept", "text/event-stream")
	resumeRec := httptest.NewRecorder()
	p.routes().ServeHTTP(resumeRec, resumeReq)
	if resumeRec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", resumeRec.Code, resumeRec.Body.String())
	}
	close(block)
}

func TestResumeAttachReservationReturnsConflictBeforeSSEStarts(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	run := newRunState("run_race", "user_001", "ch", "sk", "c", "c:0", p, nil, time.Now().Add(time.Minute))
	if !p.pending.create(run) {
		t.Fatal("create")
	}
	run.detach()
	if err := run.beginAttach(); err != nil {
		t.Fatalf("begin attach: %v", err)
	}
	defer run.cancelAttach()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages",
		strings.NewReader(`{"run_id":"run_race"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409 before SSE starts", rec.Code, rec.Body.String())
	}
}

func TestQuestionNotifyFailureDoesNotBreakTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	p := newTestPlatform(t, map[string]any{
		"token":               "secret",
		"sse_ping_interval":   "0s",
		"question_notify_url": srv.URL,
	})
	bindTestSessions(t, p)

	var mu sync.Mutex
	var got []string
	gate := make(chan struct{})
	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		mu.Lock()
		got = append(got, msg.Content)
		mu.Unlock()
		if strings.HasPrefix(msg.Content, "askq:") {
			close(release)
			scp := platform.(core.StreamingCardPlatform)
			card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = card.Finalize(context.Background(), "ok")
			return
		}
		<-gate
		_ = platform.(core.AskQuestionSender).SendAskQuestion(context.Background(), msg.ReplyCtx, core.UserQuestion{
			Question: "Q",
			Options:  []core.UserQuestionOption{{Label: "A", Value: "A"}},
		}, 0)
		<-release
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"q"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	go func() { p.routes().ServeHTTP(httptest.NewRecorder(), req) }()

	runID := waitRunID(t, p)
	cancel()
	time.Sleep(30 * time.Millisecond)
	close(gate)
	time.Sleep(50 * time.Millisecond)

	run := p.pending.get(runID)
	if run == nil {
		t.Fatal("run missing after notify failure")
	}
	run.mu.Lock()
	ixID := ""
	if run.interaction != nil {
		ixID = run.interaction.ID
	}
	run.mu.Unlock()
	if ixID == "" {
		t.Fatal("expected interaction despite notify failure")
	}
	resp := postRespond(t, p, `{"run_id":`+jsonQuote(runID)+`,"interaction_id":`+jsonQuote(ixID)+`,"answers":[{"index":0,"value":"A"}]}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("respond status=%d body=%s", resp.Code, resp.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.pending.get(runID) == nil {
			return
		}
		run := p.pending.get(runID)
		if run != nil {
			run.mu.Lock()
			done := run.finalized
			run.mu.Unlock()
			if done {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, c := range got {
		if strings.HasPrefix(c, "askq:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected askq dispatch, got %#v", got)
	}
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
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeReq.Header.Set("Accept", "text/event-stream")
	resumeRec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		p.routes().ServeHTTP(resumeRec, resumeReq)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond) // resume loop waiting
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

func TestReplayFailureKeepsLastRecoverable(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	run := newRunState("run_keep", "u", "ch", "sk", "c", "c:0", p, nil, time.Now().Add(time.Minute))
	run.detach()
	run.setLastRecoverable("text_delta", map[string]any{"message_id": "c:0", "text": "keep-me", "replace": true})

	// Simulate resume peek without successful write: must still be present.
	ev := run.peekLastRecoverable()
	if ev == nil || ev.name != "text_delta" {
		t.Fatalf("peek = %#v", ev)
	}
	// Do not clear — failed write path.
	if run.peekLastRecoverable() == nil {
		t.Fatal("cache cleared before successful replay")
	}
	run.clearLastRecoverable()
	if run.peekLastRecoverable() != nil {
		t.Fatal("expected clear after successful replay")
	}
}

func TestDetachedQuestionNotOverwrittenByText(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	gate := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		<-gate
		_ = platform.(core.AskQuestionSender).SendAskQuestion(context.Background(), msg.ReplyCtx, core.UserQuestion{
			Question: "Pick",
			Options:  []core.UserQuestionOption{{Label: "A", Value: "A"}},
		}, 0)
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
