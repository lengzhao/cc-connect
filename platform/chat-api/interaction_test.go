package chatapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func postRespond(t *testing.T, p *Platform, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/messages/respond", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	return rec
}

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

	respondBody := fmt.Sprintf(`{"run_id":%q,"interaction_id":%q,"decision":"allow"}`, runID, interactionID)
	respondRec := postRespond(t, p, respondBody)
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
	dupRec := postRespond(t, p, respondBody)
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

func TestPreferAskUserButtons_ChatAPI(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	pref, ok := any(p).(core.PreferAskUserButtons)
	if !ok || !pref.PreferAskUserButtons() {
		t.Fatal("chat-api must prefer ask-user buttons so multiSelect emits question_request")
	}
}

func TestRecordAskUserQuestionHistory_ChatAPI(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	rec, ok := any(p).(core.AskUserQuestionHistoryRecorder)
	if !ok || !rec.RecordAskUserQuestionHistory() {
		t.Fatal("chat-api must always record AskUserQuestion history")
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

	respondRec := postRespond(t, p, fmt.Sprintf(
		`{"run_id":%q,"interaction_id":%q,"answers":[{"index":0,"value":"Production"}]}`,
		runID, interactionID))
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

	var cardGroup []any
	for _, e := range parseSSE(rec.Body.String()) {
		if e.Name != "question_request" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(e.Data), &payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := payload["actions"]; ok {
			t.Fatalf("question_request must not include top-level actions: %#v", payload)
		}
		if _, ok := payload["multi_select"]; ok {
			t.Fatalf("question_request must not include top-level multi_select: %#v", payload)
		}
		cardGroup, _ = payload["card_group"].([]any)
		if len(cardGroup) != 1 {
			t.Fatalf("card_group=%#v", cardGroup)
		}
		card, _ := cardGroup[0].(map[string]any)
		if card["type"] != "single_select" {
			t.Fatalf("card.type=%#v", card["type"])
		}
		opts, _ := card["options"].([]any)
		if len(opts) < 2 {
			t.Fatalf("options=%#v", opts)
		}
		first, _ := opts[0].(map[string]any)
		if first["label"] == "" {
			t.Fatalf("first option=%#v", first)
		}
	}
	if len(cardGroup) != 1 {
		t.Fatalf("card_group missing")
	}
	close(release)
	<-done
}

func TestAskQuestionMultiSelectActions(t *testing.T) {
	ix := &interactionState{
		Kind:        interactionQuestion,
		MultiSelect: true,
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

func TestAskQuestionSingleSelectRejectsMultiOptionIDs(t *testing.T) {
	ix := &interactionState{
		Kind:        interactionQuestion,
		MultiSelect: false,
		Actions: []interactionAction{
			{ID: "askq:0:1", Label: "A"},
			{ID: "askq:0:2", Label: "B"},
			{ID: "askq:0:3", Label: "C"},
		},
	}
	_, _, err := normalizeInteractionResponse(ix, interactionRespondRequest{
		OptionIDs: []string{"0:1", "0:2", "0:3"},
	})
	if err == nil {
		t.Fatal("expected error for multi option_ids on single-select")
	}
}

func TestAskQuestionMultiSelectSSE(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	ready := make(chan struct{}, 1)
	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		if msg.Content == "/stop" {
			return
		}
		if strings.HasPrefix(msg.Content, "askq:") || msg.Content == "1,3" {
			if scp, ok := platform.(core.StreamingCardPlatform); ok {
				c, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
				_ = c.Finalize(context.Background(), "ok")
			}
			return
		}
		_ = platform.(core.InlineButtonSender).SendWithButtons(context.Background(), msg.ReplyCtx,
			"pick many",
			[][]core.ButtonOption{
				{{Text: "A", Data: "askq:0:1", MultiSelect: true}},
				{{Text: "B", Data: "askq:0:2", MultiSelect: true}},
				{{Text: "C", Data: "askq:0:3", MultiSelect: true}},
			})
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
	ixID := waitInteractionID(t, rec, "question_request")
	runID := ""
	var cardType any
	for _, e := range parseSSE(rec.Body.String()) {
		if e.Name != "question_request" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(e.Data), &payload); err != nil {
			t.Fatal(err)
		}
		cards, _ := payload["card_group"].([]any)
		if len(cards) > 0 {
			card, _ := cards[0].(map[string]any)
			cardType = card["type"]
		}
		runID, _ = payload["run_id"].(string)
	}
	if cardType != "multi_select" {
		t.Fatalf("card.type = %#v, want multi_select", cardType)
	}
	if runID == "" {
		t.Fatal("missing run_id")
	}

	respondRec := postRespond(t, p, fmt.Sprintf(
		`{"run_id":%q,"interaction_id":%q,"answers":[{"index":0,"value":["A","C"]}]}`,
		runID, ixID))
	if respondRec.Code != http.StatusOK {
		t.Fatalf("respond status=%d body=%s", respondRec.Code, respondRec.Body.String())
	}
	close(release)
	<-done
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
		Kind:        interactionQuestion,
		MultiSelect: true,
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

	badBody := fmt.Sprintf(`{"run_id":%q,"interaction_id":%q,"decision":"allow"}`, runID, interactionID)
	bad := httptest.NewRequest(http.MethodPost, "/v1/conversations/messages/respond", strings.NewReader(badBody))
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
		"",
		p,
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

	respondRec := postRespond(t, p, fmt.Sprintf(
		`{"run_id":"run_x","interaction_id":%q,"answers":[{"index":0,"value":"A"}]}`,
		interactionID))
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

	oldRec := postRespond(t, p, fmt.Sprintf(`{"run_id":%q,"interaction_id":%q,"decision":"allow"}`, runID, firstID))
	if oldRec.Code != http.StatusNotFound && oldRec.Code != http.StatusConflict {
		t.Fatalf("old respond status=%d body=%s", oldRec.Code, oldRec.Body.String())
	}

	newRec := postRespond(t, p, fmt.Sprintf(`{"run_id":%q,"interaction_id":%q,"decision":"allow"}`, runID, secondID))
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

func TestSendAskQuestion_RichSingleConfirm(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "sse_ping_interval": "0s"})
	bindTestSessions(t, p)

	ready := make(chan struct{}, 1)
	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		if strings.HasPrefix(msg.Content, "askq:") || msg.Content == "/stop" {
			if scp, ok := platform.(core.StreamingCardPlatform); ok {
				c, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
				_ = c.Finalize(context.Background(), "ok")
			}
			return
		}
		q := core.UserQuestion{
			Question:         "请选择账户",
			Description:      "已开户账户",
			Event:            "connect_account",
			AllowCustomInput: true,
			Options: []core.UserQuestionOption{
				{Label: "招行 ****1234", Value: "acc1", Tag: "推荐", TagVariant: "recommend", Description: "默认"},
				{Label: "工行 ****5678", Value: "acc2", Tag: "交易所", TagVariant: "default"},
			},
		}
		_ = platform.(core.AskQuestionSender).SendAskQuestion(context.Background(), msg.ReplyCtx, q, 0)
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
	ixID := waitInteractionID(t, rec, "question_request")
	var payload map[string]any
	for _, e := range parseSSE(rec.Body.String()) {
		if e.Name != "question_request" {
			continue
		}
		if err := json.Unmarshal([]byte(e.Data), &payload); err != nil {
			t.Fatal(err)
		}
	}
	if payload["event"] != "connect_account" {
		t.Fatalf("event=%#v", payload["event"])
	}
	if _, ok := payload["actions"]; ok {
		t.Fatalf("must not include actions: %#v", payload)
	}
	cards, _ := payload["card_group"].([]any)
	if len(cards) != 1 {
		t.Fatalf("card_group=%#v", cards)
	}
	card, _ := cards[0].(map[string]any)
	if card["title"] != "请选择账户" || card["type"] != "single_select" {
		t.Fatalf("card=%#v", card)
	}
	others, _ := card["others"].(map[string]any)
	ci, _ := others["custom_input"].(map[string]any)
	if ci["enabled"] != true {
		t.Fatalf("custom_input=%#v", others)
	}
	opts, _ := card["options"].([]any)
	a0, _ := opts[0].(map[string]any)
	if a0["value"] != "acc1" {
		t.Fatalf("option0=%#v", a0)
	}
	tag0, _ := a0["tag"].(map[string]any)
	if tag0["variant"] != "recommend" {
		t.Fatalf("option0.tag=%#v", tag0)
	}
	a1, _ := opts[1].(map[string]any)
	tag1, _ := a1["tag"].(map[string]any)
	if tag1["text"] != "交易所" || tag1["variant"] != "default" {
		t.Fatalf("option1.tag=%#v", tag1)
	}

	// answers[] contract path
	got := make(chan string, 1)
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		got <- msg.Content
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			c, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = c.Finalize(context.Background(), "ok")
		}
	})
	body := fmt.Sprintf(`{"conversation_id":%q,"run_id":%q,"interaction_id":%q,"answers":[{"index":0,"value":"acc1"}]}`,
		strings.Split(payload["message_id"].(string), ":")[0], payload["run_id"], ixID)
	respondRec := postRespond(t, p, body)
	if respondRec.Code != http.StatusOK {
		t.Fatalf("respond status=%d body=%s", respondRec.Code, respondRec.Body.String())
	}
	select {
	case content := <-got:
		if content != "askq:0:1" {
			t.Fatalf("mapped content=%q", content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for mapped answer")
	}
	close(release)
	<-done
}

func TestBuildQuestionRequestPayload_OmitsCustomInputWhenDisabled(t *testing.T) {
	payload := buildQuestionRequestPayload(&runState{
		id:        "run_1",
		messageID: "conv_1:0",
	}, &interactionState{
		ID:        "ix_1",
		Kind:      interactionQuestion,
		Prompt:    "Pick one",
		ExpiresAt: time.Unix(1780004100, 0),
		Actions: []interactionAction{
			{ID: "askq:0:1", Label: "A"},
		},
	})
	cards, _ := payload["card_group"].([]map[string]any)
	card := cards[0]
	if _, ok := card["others"]; ok {
		t.Fatalf("others must be omitted when custom input is disabled: %#v", card)
	}
}

func TestNormalizeCardAnswers_CustomInputAndUnknownValue(t *testing.T) {
	ix := &interactionState{
		Kind:        interactionQuestion,
		MultiSelect: false,
		Actions: []interactionAction{
			{ID: "askq:0:1", Label: "$5,000", Value: "5000"},
		},
	}
	content, err := normalizeCardAnswers(ix, []cardAnswer{{
		Index:       0,
		CustomInput: json.RawMessage(`3000`),
	}})
	if err != nil || content != "3000" {
		t.Fatalf("custom_input => %q err=%v", content, err)
	}
	_, err = normalizeCardAnswers(ix, []cardAnswer{{
		Index: 0,
		Value: json.RawMessage(`"nope"`),
	}})
	if !errors.Is(err, errUnknownOption) {
		t.Fatalf("unknown value err=%v", err)
	}
}

func TestSendClientFlow_EmitsSSEWithoutInteraction(t *testing.T) {
	p := newTestPlatform(t, map[string]any{})
	run := newRunState(
		"run_cf",
		"user_001",
		"channel",
		"session",
		"conv",
		"conv:0",
		"",
		p,
		nil,
		time.Now().Add(time.Minute),
	)
	if !p.pending.create(run) {
		t.Fatal("create run")
	}
	rc := run.replyContext()

	if err := p.SendClientFlow(context.Background(), rc, "connect_account", "绑定新账户", ""); err != nil {
		t.Fatalf("SendClientFlow: %v", err)
	}

	run.mu.Lock()
	events := append([]pendingSSEEvent(nil), run.pendingEvents...)
	ix := run.interaction
	run.mu.Unlock()

	if ix != nil {
		t.Fatalf("run.interaction = %#v, want nil", ix)
	}
	if len(events) != 1 || events[0].name != "client_flow" {
		t.Fatalf("events = %#v", events)
	}
	payload, ok := events[0].payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", events[0].payload)
	}
	flowID, _ := payload["flow_id"].(string)
	if !strings.HasPrefix(flowID, "flow_") {
		t.Fatalf("flow_id = %#v, want flow_ prefix", payload["flow_id"])
	}
	wantKeys := map[string]any{
		"flow_id":     flowID,
		"type":        "connect_account",
		"description": "绑定新账户",
		"run_id":      "run_cf",
		"message_id":  "conv:0",
	}
	if len(payload) != len(wantKeys) {
		t.Fatalf("payload keys = %#v, want exactly %v", payload, wantKeys)
	}
	for k, want := range wantKeys {
		if payload[k] != want {
			t.Fatalf("payload[%q] = %#v, want %#v", k, payload[k], want)
		}
	}
	for _, forbidden := range []string{"interaction_id", "expires_at", "actions", "card_group"} {
		if _, ok := payload[forbidden]; ok {
			t.Fatalf("payload must not include %s: %#v", forbidden, payload)
		}
	}
}

func TestSendClientFlow_CoexistsWithQuestionRequest(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"interaction_timeout": "1h"})
	run := newRunState(
		"run_cf_coexist",
		"user_001",
		"channel",
		"session",
		"conv",
		"conv:1",
		"",
		p,
		nil,
		time.Now().Add(time.Minute),
	)
	if !p.pending.create(run) {
		t.Fatal("create run")
	}
	rc := run.replyContext()

	q := core.UserQuestion{
		Question: "请选择账户",
		Options: []core.UserQuestionOption{
			{Label: "A", Value: "a"},
			{Label: "B", Value: "b"},
		},
	}
	if err := p.SendAskQuestion(context.Background(), rc, q, 0); err != nil {
		t.Fatalf("SendAskQuestion: %v", err)
	}

	run.mu.Lock()
	beforeIx := run.interaction
	run.mu.Unlock()
	if beforeIx == nil || beforeIx.ID == "" {
		t.Fatal("expected question interaction after SendAskQuestion")
	}
	origID := beforeIx.ID
	origPtr := beforeIx

	if err := p.SendClientFlow(context.Background(), rc, "  create_task  ", "  创建任务  ", ""); err != nil {
		t.Fatalf("SendClientFlow: %v", err)
	}

	run.mu.Lock()
	events := append([]pendingSSEEvent(nil), run.pendingEvents...)
	afterIx := run.interaction
	run.mu.Unlock()

	if afterIx != origPtr {
		t.Fatalf("interaction object changed: before=%p after=%p", origPtr, afterIx)
	}
	if afterIx.ID != origID {
		t.Fatalf("interaction ID changed: %q -> %q", origID, afterIx.ID)
	}

	var names []string
	var flowPayload map[string]any
	var questionPayload map[string]any
	for _, e := range events {
		names = append(names, e.name)
		payload, _ := e.payload.(map[string]any)
		switch e.name {
		case "client_flow":
			flowPayload = payload
		case "question_request":
			questionPayload = payload
		}
	}
	if len(names) != 2 || names[0] != "question_request" || names[1] != "client_flow" {
		t.Fatalf("event names = %v, want [question_request client_flow]", names)
	}
	if questionPayload["interaction_id"] != origID {
		t.Fatalf("question interaction_id = %#v, want %q", questionPayload["interaction_id"], origID)
	}
	if flowPayload["type"] != "create_task" || flowPayload["description"] != "创建任务" {
		t.Fatalf("client_flow payload = %#v", flowPayload)
	}
	if _, ok := flowPayload["interaction_id"]; ok {
		t.Fatalf("client_flow must not include interaction_id: %#v", flowPayload)
	}
}

func TestSendClientFlow_InvalidInput(t *testing.T) {
	p := newTestPlatform(t, map[string]any{})
	run := newRunState(
		"run_cf_invalid",
		"user_001",
		"channel",
		"session",
		"conv",
		"conv:2",
		"",
		p,
		nil,
		time.Now().Add(time.Minute),
	)
	if !p.pending.create(run) {
		t.Fatal("create run")
	}
	rc := run.replyContext()

	tests := []struct {
		name        string
		replyTo     any
		flowType    string
		description string
		wantSubstr  string
	}{
		{name: "nil reply", replyTo: nil, flowType: "connect_account", description: "x", wantSubstr: "reply context"},
		{name: "wrong type", replyTo: "not-rc", flowType: "connect_account", description: "x", wantSubstr: "reply context"},
		{name: "empty runID", replyTo: &replyContext{}, flowType: "connect_account", description: "x", wantSubstr: "reply context"},
		{name: "empty type", replyTo: rc, flowType: "  ", description: "x", wantSubstr: "invalid client_flow"},
		{name: "empty description", replyTo: rc, flowType: "connect_account", description: "  ", wantSubstr: "invalid client_flow"},
		{name: "missing pending", replyTo: &replyContext{runID: "missing_run"}, flowType: "connect_account", description: "x", wantSubstr: "not pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.SendClientFlow(context.Background(), tt.replyTo, tt.flowType, tt.description, "")
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("err=%v, want substr %q", err, tt.wantSubstr)
			}
		})
	}

	run.mu.Lock()
	n := len(run.pendingEvents)
	ix := run.interaction
	run.mu.Unlock()
	if n != 0 {
		t.Fatalf("invalid calls must not enqueue events, got %d", n)
	}
	if ix != nil {
		t.Fatalf("invalid calls must not set interaction, got %#v", ix)
	}
}

func TestSendClientFlow_EmitsArgsWhenProvided(t *testing.T) {
	p := newTestPlatform(t, map[string]any{})
	run := newRunState(
		"run_cf_args",
		"user_001",
		"channel",
		"session",
		"conv",
		"conv:2",
		"",
		p,
		nil,
		time.Now().Add(time.Minute),
	)
	if !p.pending.create(run) {
		t.Fatal("create run")
	}
	rc := run.replyContext()

	if err := p.SendClientFlow(context.Background(), rc, "create_task", "创建任务", "task_123"); err != nil {
		t.Fatalf("SendClientFlow: %v", err)
	}

	run.mu.Lock()
	payload, ok := run.pendingEvents[0].payload.(map[string]any)
	run.mu.Unlock()
	if !ok {
		t.Fatalf("payload type = %T", run.pendingEvents[0].payload)
	}
	if payload["args"] != "task_123" {
		t.Fatalf("args = %#v", payload["args"])
	}
}
