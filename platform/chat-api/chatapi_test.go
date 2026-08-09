package chatapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func newTestPlatform(t *testing.T, opts map[string]any) *Platform {
	t.Helper()
	plat, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return plat.(*Platform)
}

func bindTestSessions(t *testing.T, p *Platform) *core.SessionManager {
	t.Helper()
	sm := core.NewSessionManager("")
	p.mu.Lock()
	p.sessions = sm
	p.mu.Unlock()
	return sm
}

const testChannel = "test-channel"

func setChatReadHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Chat-API-Channel", testChannel)
}

func testChannelSessionKey() string {
	return sessionKeyForChannel(testChannel)
}

func TestNewDefaults(t *testing.T) {
	p := newTestPlatform(t, map[string]any{})
	if p.listenAddr != ":8030" {
		t.Fatalf("listenAddr = %q, want :8030", p.listenAddr)
	}
	if p.path != "/v1/" {
		t.Fatalf("path = %q, want /v1/", p.path)
	}
	if !strings.EqualFold(p.userHeader, "X-Chat-API-User") {
		t.Fatalf("userHeader = %q", p.userHeader)
	}
	if !strings.EqualFold(p.userNameHeader, "X-Chat-API-User-Name") {
		t.Fatalf("userNameHeader = %q", p.userNameHeader)
	}
	if !strings.EqualFold(p.channelHeader, "X-Chat-API-Channel") {
		t.Fatalf("channelHeader = %q", p.channelHeader)
	}
	if p.busyPolicy != busyPolicyQueue {
		t.Fatalf("busyPolicy = %q, want queue", p.busyPolicy)
	}
	if p.interactionTimeout != defaultInteractionTimeout {
		t.Fatalf("interactionTimeout = %v, want %v", p.interactionTimeout, defaultInteractionTimeout)
	}
	if p.ssePingInterval != defaultSSEPingInterval {
		t.Fatalf("ssePingInterval = %v, want %v", p.ssePingInterval, defaultSSEPingInterval)
	}
}

func TestHistoryMessagesAndMessageID(t *testing.T) {
	entries := []core.HistoryEntry{
		{Role: "user", Content: "q1", Timestamp: time.Unix(100, 0)},
		{Role: "assistant", Content: "a1", Timestamp: time.Unix(101, 0)},
		{Role: "user", Content: "q2", Timestamp: time.Unix(200, 0)},
	}
	msgs := historyMessages("s1", entries)
	if len(msgs) != 3 {
		t.Fatalf("messages len = %d, want 3", len(msgs))
	}
	if msgs[0].ID != "s1:0" || msgs[0].Role != "user" || msgs[0].Content != "q1" {
		t.Fatalf("first = %+v", msgs[0])
	}
	if msgs[2].ID != "s1:2" || msgs[2].Content != "q2" {
		t.Fatalf("third = %+v", msgs[2])
	}
	if countCompletedTurns(entries) != 1 {
		t.Fatalf("completed turns = %d, want 1", countCompletedTurns(entries))
	}
}

func TestHistoryMessagesWithUserIdentity(t *testing.T) {
	entries := []core.HistoryEntry{
		{Role: "user", Content: "q1", UserID: "uid_a", UserName: "Alice", Timestamp: time.Unix(100, 0)},
		{Role: "assistant", Content: "a1", Timestamp: time.Unix(101, 0)},
		{Role: "user", Content: "q2", UserID: "uid_b", Timestamp: time.Unix(200, 0)},
		{Role: "assistant", Content: "a2", Timestamp: time.Unix(201, 0)},
	}
	msgs := historyMessages("s1", entries)
	if len(msgs) != 4 {
		t.Fatalf("messages len = %d, want 4", len(msgs))
	}
	if msgs[0].UserID != "uid_a" || msgs[0].UserName != "Alice" {
		t.Fatalf("first user = %+v", msgs[0])
	}
	if msgs[2].UserID != "uid_b" || msgs[2].UserName != "" {
		t.Fatalf("third user = %+v", msgs[2])
	}
	api := historyMessageToAPI(msgs[0])
	if api["query"] != "q1" || api["role"] != "user" {
		t.Fatalf("api = %+v", api)
	}
}

func TestMessagesHTTPReturnsUserIdentity(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	s := sm.NewSession(testChannelSessionKey(), "chat")
	s.AddUserHistory("hello", "uid_a", "Alice")
	s.AddHistory("assistant", "hi")
	s.AddUserHistory("follow up", "uid_b", "Bob")
	s.AddHistory("assistant", "sure")

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+s.ID+"/messages?limit=10", nil)
	setChatReadHeaders(req, "secret")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			Messages []map[string]any `json:"messages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Messages) != 4 {
		t.Fatalf("messages = %+v", resp.Data.Messages)
	}
	latest := resp.Data.Messages[0]
	if latest["role"] != "assistant" || latest["content"] != "sure" {
		t.Fatalf("latest message = %+v", latest)
	}
	if latest["user_id"] != nil || latest["user_name"] != nil {
		t.Fatalf("assistant should not carry user fields = %+v", latest)
	}
	third := resp.Data.Messages[2]
	if third["role"] != "assistant" || third["content"] != "hi" {
		t.Fatalf("third message = %+v", third)
	}
	second := resp.Data.Messages[1]
	if second["user_id"] != "uid_b" || second["user_name"] != "Bob" {
		t.Fatalf("second message user fields = %+v", second)
	}
	oldest := resp.Data.Messages[3]
	if oldest["user_id"] != "uid_a" || oldest["user_name"] != "Alice" {
		t.Fatalf("oldest message user fields = %+v", oldest)
	}
}

func TestChatMessagesPassesChannelKeyToHandler(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	_ = sm.NewSession(testChannelSessionKey(), "default")

	var gotChannel string
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		gotChannel = msg.ChannelKey
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = card.Finalize(context.Background(), "ok")
		}
	})

	body := `{"query":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", "team-alpha/backend")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotChannel != "team-alpha/backend" {
		t.Fatalf("ChannelKey = %q, want team-alpha/backend", gotChannel)
	}
}

func TestChatMessagesRejectsInvalidChannel(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	bindTestSessions(t, p)

	cases := []string{
		"bad channel",
		"../escape",
		"a//b",
		".",
		"/leading",
		"trailing/",
	}
	for _, channel := range cases {
		body := `{"query":"hi"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("X-Chat-API-User", "user_001")
		req.Header.Set("X-Chat-API-Channel", channel)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		rec := httptest.NewRecorder()
		p.routes().ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("channel %q status = %d, want 400", channel, rec.Code)
		}
	}
}

func TestChatMessagesPassesUserNameToHandler(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	_ = sm.NewSession(testChannelSessionKey(), "default")

	var gotID, gotName string
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		gotID = msg.UserID
		gotName = msg.UserName
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = card.Finalize(context.Background(), "ok")
		}
	})

	body := `{"query":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("X-Chat-API-User-Name", "Alice")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotID != "user_001" || gotName != "Alice" {
		t.Fatalf("handler user = (%q, %q), want (user_001, Alice)", gotID, gotName)
	}
}

func TestSharedChannelGuestCanPostAndRead(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	ownerSession := sm.NewSession(testChannelSessionKey(), "team chat")

	var guestSessionKey string
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		guestSessionKey = msg.SessionKey
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = card.Finalize(context.Background(), "reply")
		}
	})

	body := `{"conversation_id":"` + ownerSession.ID + `","query":"from guest"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_b")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("X-Chat-API-User-Name", "Bob")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("guest post status = %d body=%s", rec.Code, rec.Body.String())
	}
	wantKey := engineSessionKey(testChannel, ownerSession.ID)
	if guestSessionKey != wantKey {
		t.Fatalf("guest session key = %q, want %q", guestSessionKey, wantKey)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/conversations?limit=10", nil)
	setChatReadHeaders(listReq, "secret")
	listRec := httptest.NewRecorder()
	p.routes().ServeHTTP(listRec, listReq)
	var listResp struct {
		Data struct {
			Conversations []conversationView `json:"conversations"`
		} `json:"data"`
	}
	_ = json.Unmarshal(listRec.Body.Bytes(), &listResp)
	if len(listResp.Data.Conversations) != 1 {
		t.Fatalf("channel list should include shared conversation, got %+v", listResp.Data.Conversations)
	}

	msgReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+ownerSession.ID+"/messages?limit=10", nil)
	setChatReadHeaders(msgReq, "secret")
	msgRec := httptest.NewRecorder()
	p.routes().ServeHTTP(msgRec, msgReq)
	if msgRec.Code != http.StatusOK {
		t.Fatalf("guest read status = %d body=%s", msgRec.Code, msgRec.Body.String())
	}
}

func TestPatchConversationWrongChannelNotFound(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	s := sm.NewSession(testChannelSessionKey(), "team chat")

	body := `{"name":"hijacked"}`
	req := httptest.NewRequest(http.MethodPatch, "/v1/conversations/"+s.ID, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_b")
	req.Header.Set("X-Chat-API-Channel", "other-channel")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("wrong channel patch status = %d, want 404", rec.Code)
	}
	if s.GetName() != "team chat" {
		t.Fatalf("name changed to %q", s.GetName())
	}
}

func TestPaginateConversations(t *testing.T) {
	sm := core.NewSessionManager("")
	s1 := sm.NewSession("chat-api:u1", "one")
	time.Sleep(2 * time.Millisecond)
	s2 := sm.NewSession("chat-api:u1", "two")
	_ = s1
	_ = s2
	sessions := sm.ListSessions("chat-api:u1")
	page, hasMore, next, err := paginateConversations(sessions, "", 1)
	if err != nil {
		t.Fatalf("paginate error = %v", err)
	}
	if len(page) != 1 || !hasMore || next == "" {
		t.Fatalf("first page = %+v hasMore=%v next=%q", page, hasMore, next)
	}
	_, _, _, err = paginateConversations(sessions, "missing", 1)
	if err == nil {
		t.Fatal("expected error for missing cursor")
	}
}

func TestPaginateMessages(t *testing.T) {
	items := []historyMessage{
		{ID: "s1:0", Index: 0},
		{ID: "s1:1", Index: 1},
		{ID: "s1:2", Index: 2},
	}
	page, hasMore, next, err := paginateMessages(items, "", 2)
	if err != nil {
		t.Fatalf("paginate error = %v", err)
	}
	if len(page) != 2 || !hasMore || next != "s1:1" {
		t.Fatalf("page = %+v hasMore=%v next=%q", page, hasMore, next)
	}
	if page[0].Index != 2 {
		t.Fatalf("newest first: got index %d", page[0].Index)
	}
}

func TestListConversationsHTTP(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	sm.NewSession(testChannelSessionKey(), "hello")

	req := httptest.NewRequest(http.MethodGet, "/v1/conversations?limit=10", nil)
	setChatReadHeaders(req, "secret")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			Conversations []conversationView `json:"conversations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || len(resp.Data.Conversations) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestChatMessagesSSEStreaming(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	_ = sm.NewSession(testChannelSessionKey(), "default")

	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp, ok := platform.(core.StreamingCardPlatform)
		if !ok {
			t.Errorf("not StreamingCardPlatform")
			return
		}
		card, err := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		if err != nil {
			t.Errorf("CreateStreamingCard: %v", err)
			return
		}
		_ = card.Update(context.Background(), "hello")
		_ = card.Finalize(context.Background(), "hello world")
	})

	body := `{"conversation_id":"","query":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	events := parseSSE(rec.Body.String())
	if !hasEvent(events, "message") || !hasEvent(events, "text_delta") || !hasEvent(events, "message_end") {
		t.Fatalf("events = %#v", events)
	}
}

func TestChatMessagesImplicitCreate(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)

	var gotKey string
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		gotKey = msg.SessionKey
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = card.Finalize(context.Background(), "ok")
		}
	})

	body := `{"query":"first message","auto_generate_name":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_new")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	sessions := sm.ListSessions(testChannelSessionKey())
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	wantKey := engineSessionKey(testChannel, sessions[0].ID)
	if gotKey != wantKey {
		t.Fatalf("session key = %q, want %q", gotKey, wantKey)
	}
	if sm.ActiveSessionID(wantKey) != sessions[0].ID {
		t.Fatalf("active session for %q = %q, want bound to conversation id", wantKey, sm.ActiveSessionID(wantKey))
	}
	if !isOpaqueConversationID(sessions[0].ID) {
		t.Fatalf("conversation id = %q, want opaque conv_*", sessions[0].ID)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sessions[0].GetName() == "first message" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("name = %q, want truncated query", sessions[0].GetName())
}

func TestChatMessagesHistoryReadableByConversationID(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)

	var engineKey string
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		engineKey = msg.SessionKey
		s := sm.GetOrCreateActive(msg.SessionKey)
		s.AddUserHistory(msg.Content, msg.UserID, msg.UserName)
		s.AddHistory("assistant", "hello back")
		sm.Save()
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = card.Finalize(context.Background(), "hello back")
		}
	})

	body := `{"query":"ping","auto_generate_name":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("post status = %d body=%s", rec.Code, rec.Body.String())
	}
	if engineKey == "" {
		t.Fatal("handler never received session key")
	}
	conversationID := conversationIDFromEngineSessionKey(engineKey)
	if conversationID == "" {
		t.Fatalf("engine session key %q did not contain conversation id", engineKey)
	}

	msgReq := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+conversationID+"/messages?limit=10", nil)
	setChatReadHeaders(msgReq, "secret")
	msgRec := httptest.NewRecorder()
	p.routes().ServeHTTP(msgRec, msgReq)
	if msgRec.Code != http.StatusOK {
		t.Fatalf("messages status = %d body=%s", msgRec.Code, msgRec.Body.String())
	}
	var resp struct {
		Data struct {
			Messages []map[string]any `json:"messages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(msgRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Messages) != 2 {
		t.Fatalf("messages = %+v, want 2 history entries", resp.Data.Messages)
	}
	var userMsg, assistantMsg map[string]any
	for _, m := range resp.Data.Messages {
		switch m["role"] {
		case "user":
			userMsg = m
		case "assistant":
			assistantMsg = m
		}
	}
	if userMsg == nil || userMsg["query"] != "ping" {
		t.Fatalf("user message = %+v", userMsg)
	}
	if assistantMsg == nil || assistantMsg["answer"] != "hello back" {
		t.Fatalf("assistant message = %+v", assistantMsg)
	}
}

func TestChatMessagesQueuedReply(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	s := sm.NewSession(testChannelSessionKey(), "default")
	s.TryLock()

	p.setHandler(func(platform core.Platform, msg *core.Message) {
		i18n := core.NewI18n(core.LangEnglish)
		_ = platform.Reply(context.Background(), msg.ReplyCtx, i18n.T(core.MsgMessageQueued))
	})

	body := `{"conversation_id":"` + s.ID + `","query":"queued"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	events := parseSSE(rec.Body.String())
	if !hasEvent(events, "message_queued") {
		t.Fatalf("events = %#v", events)
	}
	s.Unlock()
}

func TestChatMessagesRejectBusy(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret", "busy_policy": "reject"})
	sm := bindTestSessions(t, p)
	s := sm.NewSession(testChannelSessionKey(), "default")
	s.TryLock()
	defer s.Unlock()

	body := `{"conversation_id":"` + s.ID + `","query":"blocked"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestDeleteConversationNotAllowed(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	s := sm.NewSession(testChannelSessionKey(), "old")

	req := httptest.NewRequest(http.MethodDelete, "/v1/conversations/"+s.ID, nil)
	setChatReadHeaders(req, "secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if sm.FindByID(s.ID) == nil {
		t.Fatal("session deleted unexpectedly")
	}
}

func TestPatchConversation(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	sm := bindTestSessions(t, p)
	s := sm.NewSession(testChannelSessionKey(), "old")

	body := `{"name":"renamed"}`
	req := httptest.NewRequest(http.MethodPatch, "/v1/conversations/"+s.ID, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if s.GetName() != "renamed" {
		t.Fatalf("name = %q", s.GetName())
	}
}

func TestHookContextMetadata(t *testing.T) {
	p := newTestPlatform(t, nil)
	rc := &replyContext{metadata: map[string]any{"tenant": "acme"}}
	got := p.HookContext(rc)
	if got.Context["tenant"] != "acme" {
		t.Fatalf("context = %#v", got.Context)
	}
}

func TestCancelRunEndpoint(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	bindTestSessions(t, p)

	block := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		<-block
	})

	body := `{"query":"block"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	go func() {
		rec := httptest.NewRecorder()
		p.routes().ServeHTTP(rec, req)
	}()

	var runID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.pending.mu.Lock()
		for id := range p.pending.runs {
			runID = id
			break
		}
		p.pending.mu.Unlock()
		if runID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if runID == "" {
		t.Fatal("expected active run")
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/cancel", nil)
	cancelReq.Header.Set("Authorization", "Bearer secret")
	cancelReq.Header.Set("X-Chat-API-User", "user_001")
	cancelReq.Header.Set("X-Chat-API-Channel", testChannel)
	cancelRec := httptest.NewRecorder()
	p.routes().ServeHTTP(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", cancelRec.Code, cancelRec.Body.String())
	}
	if p.pending.get(runID) != nil {
		t.Fatal("run still active after cancel")
	}
	close(block)
}

func TestDisconnectDoesNotRemoveRun(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"token": "secret"})
	bindTestSessions(t, p)

	release := make(chan struct{})
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		<-release
		if scp, ok := platform.(core.StreamingCardPlatform); ok {
			card, _ := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
			_ = card.Finalize(context.Background(), "done")
		}
	})

	body := `{"query":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat-messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "user_001")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	var runID string
	go func() {
		rec := httptest.NewRecorder()
		p.routes().ServeHTTP(rec, req)
	}()

	time.Sleep(30 * time.Millisecond)
	for id, run := range p.pending.runs {
		runID = id
		_ = run
		break
	}
	if runID == "" {
		t.Fatal("expected active run")
	}

	cancel()
	time.Sleep(30 * time.Millisecond)

	if p.pending.get(runID) == nil {
		t.Fatal("run removed on disconnect, want it to keep processing")
	}
	close(release)
	time.Sleep(50 * time.Millisecond)
	if p.pending.get(runID) != nil {
		t.Fatal("run should be removed after turn completes")
	}
}

type sseEvent struct {
	Name string
	Data string
}

func parseSSE(body string) []sseEvent {
	var events []sseEvent
	scanner := bufio.NewScanner(bytes.NewBufferString(body))
	var current sseEvent
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			current.Name = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			current.Data = strings.TrimPrefix(line, "data: ")
		}
		if line == "" && current.Name != "" {
			events = append(events, current)
			current = sseEvent{}
		}
	}
	return events
}

func hasEvent(events []sseEvent, name string) bool {
	for _, e := range events {
		if e.Name == name {
			return true
		}
	}
	return false
}
