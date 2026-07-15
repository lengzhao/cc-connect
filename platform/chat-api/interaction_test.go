package chatapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestPermissionRequestSSEAndRespond(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	bindTestSessions(t, p)

	var (
		mu       sync.Mutex
		messages []*core.Message
	)
	permReady := make(chan string, 1)
	release := make(chan struct{})

	p.setHandler(func(platform core.Platform, msg *core.Message) {
		mu.Lock()
		messages = append(messages, msg)
		mu.Unlock()

		if msg.Content == "/stop" {
			return
		}
		if msg.IsPermissionResponse || msg.Content == "allow" || msg.Content == "deny" || msg.Content == "allow all" {
			_ = platform.Reply(context.Background(), msg.ReplyCtx, "permission decision accepted")
			return
		}
		bs, ok := platform.(core.InlineButtonSender)
		if !ok {
			t.Error("platform is not InlineButtonSender")
			close(release)
			return
		}
		_ = bs.SendWithButtons(context.Background(), msg.ReplyCtx, "Allow Bash?", [][]core.ButtonOption{
			{
				{Text: "Allow", Data: "perm:allow"},
				{Text: "Deny", Data: "perm:deny"},
			},
			{
				{Text: "Allow All", Data: "perm:allow_all"},
			},
		})
		rc := msg.ReplyCtx.(*replyContext)
		permReady <- rc.runID
		<-release
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = card.Finalize(context.Background(), "done after allow")
		}
	})

	body := `{"query":"run bash"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		p.routes().ServeHTTP(rec, req)
		close(done)
	}()

	var runID string
	select {
	case runID = <-permReady:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for permission prompt")
	}

	// Wait until permission_request is flushed to SSE body.
	deadline := time.Now().Add(2 * time.Second)
	var events []sseEvent
	var interactionID string
	for time.Now().Before(deadline) {
		events = parseSSE(rec.Body.String())
		for _, e := range events {
			if e.Name != "permission_request" {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(e.Data), &payload); err != nil {
				t.Fatalf("decode permission_request: %v", err)
			}
			interactionID, _ = payload["interaction_id"].(string)
			if _, ok := payload["expires_at"].(float64); !ok {
				t.Fatalf("missing expires_at: %#v", payload)
			}
			break
		}
		if interactionID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if interactionID == "" {
		t.Fatalf("missing permission_request event: %#v body=%s", events, rec.Body.String())
	}

	respondBody := `{"decision":"allow"}`
	respondReq := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/interactions/"+interactionID+"/respond", strings.NewReader(respondBody))
	respondReq.Header.Set("Authorization", "Bearer secret")
	respondReq.Header.Set("X-Chat-API-User", "user_001")
	respondReq.Header.Set("Content-Type", "application/json")
	respondRec := httptest.NewRecorder()
	p.routes().ServeHTTP(respondRec, respondReq)
	if respondRec.Code != http.StatusOK {
		t.Fatalf("respond status = %d body=%s", respondRec.Code, respondRec.Body.String())
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := false
		for _, m := range messages {
			if m.Content == "allow" && m.IsPermissionResponse {
				found = true
				break
			}
		}
		mu.Unlock()
		if found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	foundAllow := false
	for _, m := range messages {
		if m.Content == "allow" && m.IsPermissionResponse {
			foundAllow = true
		}
	}
	mu.Unlock()
	if !foundAllow {
		t.Fatalf("expected allow permission message, got %#v", messages)
	}

	// Respond must not end the run early.
	if hasEvent(parseSSE(rec.Body.String()), "message_end") {
		t.Fatal("message_end appeared before handler finished")
	}

	// Duplicate respond while run still active → 409
	dupRec := httptest.NewRecorder()
	p.routes().ServeHTTP(dupRec, respondReq)
	if dupRec.Code != http.StatusConflict {
		t.Fatalf("dup status = %d, want 409 body=%s", dupRec.Code, dupRec.Body.String())
	}

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE did not finish")
	}
	if !hasEvent(parseSSE(rec.Body.String()), "message_end") {
		t.Fatalf("missing message_end: %s", rec.Body.String())
	}
	if !hasEvent(parseSSE(rec.Body.String()), "interaction_ack") {
		t.Fatalf("missing interaction_ack after respond: %s", rec.Body.String())
	}
}

func TestAskQuestionRequestSSEAndRespond(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	bindTestSessions(t, p)

	var (
		mu          sync.Mutex
		gotContents []string
	)
	questionReady := make(chan string, 1)

	p.setHandler(func(platform core.Platform, msg *core.Message) {
		mu.Lock()
		gotContents = append(gotContents, msg.Content)
		mu.Unlock()
		if msg.Content == "/stop" {
			return
		}
		// Responding to AskUserQuestion continues the same SSE stream.
		if msg.Content == "askq:0:2" || msg.Content == "Production" {
			if msg.IsPermissionResponse {
				t.Error("question response must not be marked as permission")
			}
			if scp, ok := platform.(core.StreamingCardPlatform); ok {
				c, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
				_ = c.Finalize(context.Background(), "deployed")
			}
			return
		}
		card := core.NewCard().
			Title("Question", "blue").
			Markdown("**Pick env**").
			ListItemBtnExtra("Staging — pre", "Staging", "default", "askq:0:1", nil).
			ListItemBtnExtra("Production — prod", "Production", "default", "askq:0:2", nil).
			Build()
		_ = platform.(core.CardSender).ReplyCard(context.Background(), msg.ReplyCtx, card)
		questionReady <- msg.ReplyCtx.(*replyContext).runID
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"deploy"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		p.routes().ServeHTTP(rec, req)
		close(done)
	}()

	var runID string
	select {
	case runID = <-questionReady:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for question prompt")
	}

	interactionID := waitInteractionID(t, rec, "question_request")
	events := parseSSE(rec.Body.String())
	if !hasEvent(events, "question_request") {
		t.Fatalf("missing question_request: %#v", events)
	}
	if hasEvent(events, "message_end") {
		t.Fatalf("question_request ended SSE before response: %#v", events)
	}

	respondReq := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/interactions/"+interactionID+"/respond",
		strings.NewReader(`{"option_id":"0:2"}`))
	respondReq.Header.Set("Authorization", "Bearer secret")
	respondReq.Header.Set("X-Chat-API-User", "user_001")
	respondReq.Header.Set("Content-Type", "application/json")
	respondRec := httptest.NewRecorder()
	p.routes().ServeHTTP(respondRec, respondReq)
	if respondRec.Code != http.StatusOK {
		t.Fatalf("respond status = %d body=%s", respondRec.Code, respondRec.Body.String())
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE did not finish")
	}

	events = parseSSE(rec.Body.String())
	if !hasEvent(events, "message_end") {
		t.Fatalf("missing message_end: %#v body=%s", events, rec.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, c := range gotContents {
		if c == "askq:0:2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected answer content, got %#v", gotContents)
	}
}

func TestPublicizeActionID(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"perm:allow", "allow"},
		{"perm:deny", "deny"},
		{"perm:allow_all", "allow_all"},
		{"askq:0:2", "0:2"},
		{"0:1", "0:1"},
		{"allow", "allow"},
	}
	for _, tt := range tests {
		if got := publicActionID(tt.in); got != tt.want {
			t.Fatalf("publicActionID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEmitInteractionPublicActionIDs(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	ready := make(chan struct{}, 1)
	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		if msg.IsPermissionResponse || msg.Content == "allow" || msg.Content == "/stop" {
			return
		}
		card := core.NewCard().Title("Q", "blue").Markdown("**Pick**").
			ListItemBtnExtra("Staging — pre", "Staging", "default", "askq:0:1", nil).
			ListItemBtnExtra("Production — prod", "Production", "default", "askq:0:2", nil).
			Build()
		_ = platform.(core.CardSender).ReplyCard(context.Background(), msg.ReplyCtx, card)
		ready <- struct{}{}
		<-release
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			c, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = c.Finalize(context.Background(), "done")
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"q"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		p.routes().ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
	_ = waitInteractionID(t, rec, "question_request")

	var actions []any
	for _, e := range parseSSE(rec.Body.String()) {
		if e.Name != "question_request" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(e.Data), &payload); err != nil {
			t.Fatal(err)
		}
		actions, _ = payload["actions"].([]any)
		if _, ok := payload["multi_select"]; ok {
			t.Fatalf("multi_select should be removed: %#v", payload)
		}
		if _, ok := payload["description"]; ok {
			// top-level description unexpected
		}
	}
	if len(actions) < 2 {
		t.Fatalf("actions = %#v", actions)
	}
	first, _ := actions[0].(map[string]any)
	if first["id"] != "0:1" {
		t.Fatalf("first action id = %#v, want 0:1", first["id"])
	}
	if _, ok := first["description"]; ok {
		t.Fatalf("description enrichment should be gone: %#v", first)
	}
	close(release)
	<-done
}

func TestAskQuestionMultiSelectActions(t *testing.T) {
	ix := &interactionState{
		Kind: interactionQuestion,
		Actions: []interactionAction{
			{ID: "askq:0:1", Label: "A"},
			{ID: "askq:0:2", Label: "B"},
			{ID: "askq:0:3", Label: "C"},
		},
	}
	content, isPerm, err := normalizeInteractionResponse(ix, interactionRespondRequest{
		OptionIDs: []string{"0:1", "0:3"},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if isPerm {
		t.Fatal("expected not permission")
	}
	if content != "1,3" {
		t.Fatalf("content = %q, want 1,3", content)
	}
}

func TestNormalizeInteractionResponse_PublicDecision(t *testing.T) {
	ix := &interactionState{Kind: interactionPermission}
	content, isPerm, err := normalizeInteractionResponse(ix, interactionRespondRequest{
		Decision: "allow",
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !isPerm || content != "allow" {
		t.Fatalf("got content=%q isPerm=%v", content, isPerm)
	}
	content, isPerm, err = normalizeInteractionResponse(ix, interactionRespondRequest{Decision: "allow_all"})
	if err != nil || !isPerm || content != "allow all" {
		t.Fatalf("allow_all: content=%q isPerm=%v err=%v", content, isPerm, err)
	}
}

func TestNormalizeInteractionResponse_PublicOptionIDs(t *testing.T) {
	ix := &interactionState{
		Kind: interactionQuestion,
		Actions: []interactionAction{
			{ID: "askq:0:1", Label: "A"},
			{ID: "askq:0:2", Label: "B"},
			{ID: "askq:0:3", Label: "C"},
		},
	}
	content, isPerm, err := normalizeInteractionResponse(ix, interactionRespondRequest{
		OptionIDs: []string{"0:1", "0:3"},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if isPerm || content != "1,3" {
		t.Fatalf("got content=%q isPerm=%v", content, isPerm)
	}
	content, isPerm, err = normalizeInteractionResponse(ix, interactionRespondRequest{OptionID: "0:2"})
	if err != nil || isPerm || content != "askq:0:2" {
		t.Fatalf("option_id: content=%q isPerm=%v err=%v", content, isPerm, err)
	}
}

func TestNormalizeInteractionResponse_RejectsLegacyAction(t *testing.T) {
	ix := &interactionState{Kind: interactionPermission}
	var body interactionRespondRequest
	if err := json.Unmarshal([]byte(`{"action":"perm:allow"}`), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, _, err := normalizeInteractionResponse(ix, body); err == nil {
		t.Fatal("expected error for legacy action")
	}

	q := &interactionState{Kind: interactionQuestion}
	body = interactionRespondRequest{}
	if err := json.Unmarshal([]byte(`{"actions":["askq:0:1"]}`), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, _, err := normalizeInteractionResponse(q, body); err == nil {
		t.Fatal("expected error for legacy actions")
	}
}

func TestNormalizeInteractionResponse_RejectsOptionWithoutKnownAction(t *testing.T) {
	ix := &interactionState{Kind: interactionQuestion}
	if _, _, err := normalizeInteractionResponse(ix, interactionRespondRequest{OptionID: "0:1"}); !errors.Is(err, errUnknownOption) {
		t.Fatalf("err = %v, want %v", err, errUnknownOption)
	}
}

func TestInteractionRespondWrongUser(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "interaction_timeout": "1h"})
	bindTestSessions(t, p)

	ready := make(chan string, 1)
	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		if msg.IsPermissionResponse || msg.Content == "allow" || msg.Content == "deny" || msg.Content == "/stop" {
			return
		}
		_ = platform.(core.InlineButtonSender).SendWithButtons(context.Background(), msg.ReplyCtx, "Allow?", [][]core.ButtonOption{
			{{Text: "Allow", Data: "perm:allow"}, {Text: "Deny", Data: "perm:deny"}},
		})
		ready <- msg.ReplyCtx.(*replyContext).runID
		<-release
		_ = platform.(core.StreamingCardPlatform)
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			c, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = c.Finalize(context.Background(), "x")
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"x"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		p.routes().ServeHTTP(rec, req)
		close(done)
	}()
	runID := <-ready
	interactionID := waitInteractionID(t, rec, "permission_request")

	bad := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/interactions/"+interactionID+"/respond",
		strings.NewReader(`{"decision":"allow"}`))
	bad.Header.Set("Authorization", "Bearer secret")
	bad.Header.Set("X-Chat-API-User", "other_user")
	bad.Header.Set("Content-Type", "application/json")
	badRec := httptest.NewRecorder()
	p.routes().ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", badRec.Code)
	}

	close(release)
	<-done
}

func TestInteractionAckUsesRespondedInteractionID(t *testing.T) {
	p := newTestPlatform(t, map[string]any{})
	run := newRunState(
		"run_ack",
		"user_001",
		"channel",
		"session",
		"conv",
		"conv:0",
		nil,
		time.Now().Add(time.Minute),
	)
	run.interaction = &interactionState{
		ID:        "ix_responded",
		Kind:      interactionPermission,
		ExpiresAt: time.Now().Add(time.Minute),
		Responded: true,
	}
	if !p.pending.create(run) {
		t.Fatal("create run")
	}
	rc := run.interactionReplyContext("ix_responded")
	run.mu.Lock()
	run.interaction = &interactionState{
		ID:        "ix_new",
		Kind:      interactionQuestion,
		ExpiresAt: time.Now().Add(time.Minute),
	}
	run.mu.Unlock()

	if err := p.Reply(context.Background(), rc, "ack"); err != nil {
		t.Fatalf("reply: %v", err)
	}
	run.mu.Lock()
	events := append([]pendingSSEEvent(nil), run.pendingEvents...)
	run.mu.Unlock()
	if len(events) != 1 || events[0].name != "interaction_ack" {
		t.Fatalf("events = %#v", events)
	}
	payload, ok := events[0].payload.(map[string]any)
	if !ok {
		t.Fatalf("payload = %#v", events[0].payload)
	}
	if payload["interaction_id"] != "ix_responded" {
		t.Fatalf("interaction_id = %#v, want ix_responded", payload["interaction_id"])
	}
}

func TestPermissionInteractionTimeoutAutoDeny(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"token":               "secret",
		"interaction_timeout": "50ms",
	})
	bindTestSessions(t, p)

	var (
		mu       sync.Mutex
		messages []*core.Message
	)
	ready := make(chan struct{}, 1)
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		mu.Lock()
		messages = append(messages, msg)
		mu.Unlock()
		if msg.IsPermissionResponse || msg.Content == "deny" || msg.Content == "/stop" {
			if scp, ok := platform.(core.StreamingCardPlatform); ok && msg.Content == "deny" {
				c, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
				_ = c.Finalize(context.Background(), "denied flow")
			}
			return
		}
		_ = platform.(core.InlineButtonSender).SendWithButtons(context.Background(), msg.ReplyCtx, "Allow?", [][]core.ButtonOption{
			{{Text: "Allow", Data: "perm:allow"}, {Text: "Deny", Data: "perm:deny"}},
		})
		ready <- struct{}{}
		// Keep turn open until auto-deny + finalize.
		time.Sleep(300 * time.Millisecond)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"x"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	select {
	case <-ready:
	default:
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, m := range messages {
			if m.Content == "deny" && m.IsPermissionResponse {
				mu.Unlock()
				goto found
			}
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected auto-deny permission message, got %#v body=%s", messages, rec.Body.String())
found:
	// Ensure deny was synthesized, not a plain user prompt containing the permission card text alone as query.
	for _, m := range messages {
		if m.Content == "Allow?" && !m.IsPermissionResponse {
			t.Fatal("permission prompt was wrongly treated as user message")
		}
	}
}

func TestQuestionInteractionTimeoutCancelsTurn(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"token":               "secret",
		"interaction_timeout": "50ms",
	})
	bindTestSessions(t, p)

	var (
		mu      sync.Mutex
		stopped bool
	)
	ready := make(chan struct{}, 1)
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		if msg.Content == "/stop" {
			mu.Lock()
			stopped = true
			mu.Unlock()
			return
		}
		card := core.NewCard().Title("Q", "blue").Markdown("**Choose**").
			ListItemBtn("A", "A", "default", "askq:0:1").Build()
		_ = platform.(core.CardSender).ReplyCard(context.Background(), msg.ReplyCtx, card)
		ready <- struct{}{}
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"ask"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		p.routes().ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for interaction")
	}

	interactionID := waitInteractionID(t, rec, "question_request")
	events := parseSSE(rec.Body.String())
	if !hasEvent(events, "question_request") {
		t.Fatalf("missing question_request: %#v", events)
	}
	if hasEvent(events, "message_end") {
		t.Fatalf("question interaction ended before timeout: %#v", events)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := stopped
		mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	if !stopped {
		t.Fatal("expected /stop after question interaction timeout")
	}
	mu.Unlock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE did not finish after question timeout")
	}

	events = parseSSE(rec.Body.String())
	foundKind := false
	for _, e := range events {
		if e.Name != "error" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(e.Data), &payload); err != nil {
			t.Fatalf("decode error: %v", err)
		}
		if payload["error"] == "interaction timed out" && payload["kind"] == "question" {
			foundKind = true
		}
	}
	if !foundKind {
		t.Fatalf("missing structured interaction timeout error: %#v", events)
	}

	respondReq := httptest.NewRequest(http.MethodPost, "/v1/runs/run_x/interactions/"+interactionID+"/respond",
		strings.NewReader(`{"option_id":"0:1"}`))
	respondReq.Header.Set("Authorization", "Bearer secret")
	respondReq.Header.Set("X-Chat-API-User", "user_001")
	respondReq.Header.Set("Content-Type", "application/json")
	respondRec := httptest.NewRecorder()
	p.routes().ServeHTTP(respondRec, respondReq)
	if respondRec.Code != http.StatusNotFound {
		t.Fatalf("expired respond status = %d body=%s", respondRec.Code, respondRec.Body.String())
	}
}

func waitInteractionID(t *testing.T, rec *httptest.ResponseRecorder, eventName string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if id := waitInteractionIDFromBody(t, rec.Body.String(), eventName); id != "" {
			return id
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("missing %s in SSE: %s", eventName, rec.Body.String())
	return ""
}

func waitInteractionIDFromBody(t *testing.T, body, eventName string) string {
	t.Helper()
	for _, e := range parseSSE(body) {
		if e.Name != eventName {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(e.Data), &payload); err != nil {
			t.Fatalf("decode %s: %v", eventName, err)
		}
		id, _ := payload["interaction_id"].(string)
		if id != "" {
			if _, ok := payload["expires_at"]; !ok {
				t.Fatalf("%s missing expires_at: %#v", eventName, payload)
			}
			return id
		}
	}
	return ""
}

func TestSSEPingWhileWaitingPermission(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"token":             "secret",
		"sse_ping_interval": "20ms",
	})
	bindTestSessions(t, p)

	ready := make(chan struct{}, 1)
	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		if msg.IsPermissionResponse || msg.Content == "allow" || msg.Content == "deny" || msg.Content == "/stop" {
			return
		}
		_ = platform.(core.InlineButtonSender).SendWithButtons(context.Background(), msg.ReplyCtx, "Allow?", [][]core.ButtonOption{
			{{Text: "Allow", Data: "perm:allow"}, {Text: "Deny", Data: "perm:deny"}},
		})
		ready <- struct{}{}
		<-release
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			c, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = c.Finalize(context.Background(), "done")
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"x"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		p.routes().ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for permission")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasEvent(parseSSE(rec.Body.String()), "ping") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !hasEvent(parseSSE(rec.Body.String()), "ping") {
		t.Fatalf("missing ping event: %s", rec.Body.String())
	}
	close(release)
	<-done
}

func TestInteractionSuperseded(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	ready := make(chan string, 1)
	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		if msg.IsPermissionResponse || msg.Content == "allow" || msg.Content == "deny" || msg.Content == "/stop" {
			return
		}
		bs := platform.(core.InlineButtonSender)
		_ = bs.SendWithButtons(context.Background(), msg.ReplyCtx, "Allow first?", [][]core.ButtonOption{
			{{Text: "Allow", Data: "perm:allow"}, {Text: "Deny", Data: "perm:deny"}},
		})
		_ = bs.SendWithButtons(context.Background(), msg.ReplyCtx, "Allow second?", [][]core.ButtonOption{
			{{Text: "Allow", Data: "perm:allow"}, {Text: "Deny", Data: "perm:deny"}},
		})
		ready <- msg.ReplyCtx.(*replyContext).runID
		<-release
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			c, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = c.Finalize(context.Background(), "done")
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"x"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		p.routes().ServeHTTP(rec, req)
		close(done)
	}()
	runID := <-ready

	deadline := time.Now().Add(2 * time.Second)
	var firstID, secondID string
	for time.Now().Before(deadline) {
		events := parseSSE(rec.Body.String())
		if !hasEvent(events, "interaction_superseded") {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		var permIDs []string
		for _, e := range events {
			if e.Name != "permission_request" {
				continue
			}
			var payload map[string]any
			_ = json.Unmarshal([]byte(e.Data), &payload)
			if id, _ := payload["interaction_id"].(string); id != "" {
				permIDs = append(permIDs, id)
			}
		}
		if len(permIDs) >= 2 {
			firstID, secondID = permIDs[0], permIDs[len(permIDs)-1]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if firstID == "" || secondID == "" || firstID == secondID {
		t.Fatalf("expected two permission_request ids, body=%s", rec.Body.String())
	}
	if !hasEvent(parseSSE(rec.Body.String()), "interaction_superseded") {
		t.Fatalf("missing interaction_superseded: %s", rec.Body.String())
	}

	oldReq := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/interactions/"+firstID+"/respond",
		strings.NewReader(`{"decision":"allow"}`))
	oldReq.Header.Set("Authorization", "Bearer secret")
	oldReq.Header.Set("X-Chat-API-User", "user_001")
	oldReq.Header.Set("Content-Type", "application/json")
	oldRec := httptest.NewRecorder()
	p.routes().ServeHTTP(oldRec, oldReq)
	if oldRec.Code != http.StatusNotFound && oldRec.Code != http.StatusConflict {
		t.Fatalf("old respond status=%d body=%s", oldRec.Code, oldRec.Body.String())
	}

	newReq := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/interactions/"+secondID+"/respond",
		strings.NewReader(`{"decision":"allow"}`))
	newReq.Header.Set("Authorization", "Bearer secret")
	newReq.Header.Set("X-Chat-API-User", "user_001")
	newReq.Header.Set("Content-Type", "application/json")
	newRec := httptest.NewRecorder()
	p.routes().ServeHTTP(newRec, newReq)
	if newRec.Code != http.StatusOK {
		t.Fatalf("new respond status=%d body=%s", newRec.Code, newRec.Body.String())
	}
	close(release)
	<-done
}

func TestPlainCardDoesNotEmitQuestionRequest(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	p.setHandler(func(platform core.Platform, msg *core.Message) {
		card := core.NewCard().Title("Info", "blue").Markdown("just a note").Build()
		_ = platform.(core.CardSender).ReplyCard(context.Background(), msg.ReplyCtx, card)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(`{"query":"hi"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	events := parseSSE(rec.Body.String())
	if hasEvent(events, "question_request") || hasEvent(events, "permission_request") {
		t.Fatalf("plain card should not emit interaction: %#v", events)
	}
	if !hasEvent(events, "message_end") {
		t.Fatalf("expected message_end: %#v", events)
	}
}
