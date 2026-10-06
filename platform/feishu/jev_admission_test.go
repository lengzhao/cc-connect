package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestParseJevChatAllowlist(t *testing.T) {
	cases := []struct {
		raw     string
		chat    string
		wantOn  bool
		wantHit bool
	}{
		{"", "oc_a", false, false},
		{"all", "oc_a", true, true},
		{"oc_a, oc_b", "oc_b", true, true},
		{"oc_a", "oc_other", true, false},
	}
	for _, tc := range cases {
		got := parseJevChatAllowlist(tc.raw)
		if got.on != tc.wantOn {
			t.Fatalf("raw=%q on=%v want %v", tc.raw, got.on, tc.wantOn)
		}
		if hit := got.matches(tc.chat); hit != tc.wantHit {
			t.Fatalf("raw=%q matches(%q)=%v want %v", tc.raw, tc.chat, hit, tc.wantHit)
		}
	}
}

func TestCallJevAdmission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jevAdmitRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		admitted := req.Message == "yes"
		_ = json.NewEncoder(w).Encode(jevAdmitResponse{Admitted: admitted, Reason: "test"})
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Timeout: time.Second}
	if !callJevAdmission(context.Background(), client, srv.URL, "yes", "oc_1", "", "u1") {
		t.Fatal("expected admit")
	}
	if callJevAdmission(context.Background(), client, srv.URL, "no", "oc_1", "", "u1") {
		t.Fatal("expected drop")
	}
	if callJevAdmission(context.Background(), client, "", "yes", "oc_1", "", "u1") {
		t.Fatal("empty url must fail closed")
	}
}

func TestExtractTextForJev(t *testing.T) {
	got := extractTextForJev("text", `{"text":"hello world"}`, nil, "")
	if got != "hello world" {
		t.Fatalf("got %q", got)
	}
	if got := extractTextForJev("image", `{}`, nil, ""); got != "" {
		t.Fatalf("image should be empty, got %q", got)
	}
}

func TestJevThreadEngagement(t *testing.T) {
	p := &Platform{}
	if p.isJevThreadEngaged("oc_a", "om_root", "") {
		t.Fatal("expected not engaged before mark")
	}
	p.markJevThreadEngaged("oc_a", "om_root")
	if !p.isJevThreadEngaged("oc_a", "om_root", "") {
		t.Fatal("expected engaged by root_id")
	}
	if !p.isJevThreadEngaged("oc_a", "", "om_root") {
		t.Fatal("expected engaged by thread_id fallback")
	}
	if p.isJevThreadEngaged("oc_a", "om_other", "om_other") {
		t.Fatal("other root must not match")
	}
	if p.isJevThreadEngaged("oc_b", "om_root", "") {
		t.Fatal("other chat must not match")
	}
}

func TestNewPlatformParsesJevOptions(t *testing.T) {
	p, err := newPlatform("feishu", "https://open.feishu.cn", map[string]any{
		"app_id":                   "cli_test",
		"app_secret":               "sec",
		"require_mention":          true,
		"jev_channel_admission":    true,
		"jev_admission_url":        "http://127.0.0.1:8020/jev/admit",
		"jev_channel_chats":        "oc_test",
		"jev_admission_timeout_ms": 5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	fp := extractBasePlatform(p)
	if fp == nil {
		t.Fatal("expected *Platform")
	}
	if !fp.jevChannelAdmission {
		t.Fatal("expected jevChannelAdmission")
	}
	if !fp.jevChannelChats.matches("oc_test") {
		t.Fatal("expected chat match")
	}
	if fp.jevAdmissionURL != "http://127.0.0.1:8020/jev/admit" {
		t.Fatalf("url = %q", fp.jevAdmissionURL)
	}
	if fp.jevAdmissionHTTP == nil || fp.jevAdmissionHTTP.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v", fp.jevAdmissionHTTP.Timeout)
	}
}

func TestJevResponseScopeRoutesGroupMessages(t *testing.T) {
	for _, tc := range []struct {
		name        string
		messageType string
		scope       string
		rootID      string
		engaged     bool
		admit       bool
		wantCalls   int
		wantRoute   bool
		wantKey     string
		wantThread  bool
		mentioned   bool
		recalled    bool
		recallError bool
		deniedChat  bool
		quiet       bool
	}{
		{name: "quiet blocks explicit mention", scope: "channel", mentioned: true, quiet: true, wantCalls: 1},
		{name: "channel attachment cannot bypass guards", scope: "channel", rootID: "om_root", messageType: "image", admit: true},
		{name: "channel withdrawn during admission", scope: "channel", admit: true, wantCalls: 1, recalled: true},
		{name: "channel recall status unavailable", scope: "channel", admit: true, wantCalls: 1, recallError: true},
		{name: "channel not authorized never reaches Jev", scope: "channel", admit: true, deniedChat: true},
		{name: "channel mentioned top level", scope: "channel", mentioned: true, wantCalls: 1, wantRoute: true, wantKey: "lark:oc_test:root:om_message", wantThread: true},
		{name: "channel top level", scope: "channel", admit: true, wantCalls: 1, wantRoute: true, wantKey: "lark:oc_test:root:om_message", wantThread: true},
		{name: "channel unengaged thread", scope: "channel", rootID: "om_root", admit: true, wantCalls: 1, wantRoute: true, wantKey: "lark:oc_test:root:om_root", wantThread: true},
		{name: "channel rejected despite group reply all", scope: "channel", admit: false, wantCalls: 1},
		{name: "thread top level", scope: "thread", admit: true},
		{name: "thread unengaged", scope: "thread", rootID: "om_root", admit: true},
		{name: "thread engaged", scope: "thread", rootID: "om_root", engaged: true, admit: true, wantCalls: 1, wantRoute: true, wantKey: "lark:oc_test:root:om_root", wantThread: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			reactions := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					_, _ = w.Write([]byte(`{"code":0,"expire":7200,"tenant_access_token":"test"}`))
					return
				case "/open-apis/im/v1/messages/om_message":
					if tc.recallError {
						_, _ = w.Write([]byte(`{"code":99991672,"msg":"permission denied"}`))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"message_id": "om_message", "deleted": tc.recalled}}}})
					return
				case "/open-apis/im/v1/messages/om_message/reply":
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["reply_in_thread"] != true {
						t.Error("reply must stay under trigger")
					}
					_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_answer"}}`))
					return
				case "/open-apis/im/v1/messages/om_message/reactions":
					var body struct {
						ReactionType struct {
							EmojiType string `json:"emoji_type"`
						} `json:"reaction_type"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					reactions <- body.ReactionType.EmojiType
					_, _ = w.Write([]byte(`{"code":0,"data":{"reaction_id":"r_done"}}`))
					return
				}
				if r.URL.Path != "/" {
					_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
					return
				}
				calls.Add(1)
				var payload jevAdmitRequest
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if tc.mentioned && payload.Action != "activity" {
					t.Error("mention must only record activity")
				}
				if !tc.mentioned && (payload.CreatedAtMS == 0 || payload.MessageID != "om_message") {
					t.Error("missing age/dedup metadata")
				}
				if tc.quiet {
					_ = json.NewEncoder(w).Encode(jevAdmitResponse{Reason: "quiet"})
					return
				}
				_ = json.NewEncoder(w).Encode(jevAdmitResponse{Admitted: tc.admit})
			}))
			defer srv.Close()
			pAny, err := newPlatform("lark", "https://open.larksuite.com", map[string]any{
				"app_id": "cli_test", "app_secret": "secret",
				"jev_channel_admission": true, "jev_channel_chats": "oc_test",
				"jev_admission_url": srv.URL, "jev_response_scope": tc.scope,
				"require_mention": true, "group_reply_all": true, "thread_isolation": true,
			})
			if err != nil {
				t.Fatal(err)
			}
			p := pAny.(*interactivePlatform)
			p.botOpenID = "ou_bot"
			if tc.messageType == "image" {
				p.markThreadSessionActive("lark:oc_test:root:om_root")
			}
			if tc.deniedChat {
				p.allowChat = "oc_other"
			}
			p.doneEmoji = "Done"
			p.client = lark.NewClient("jev-done-"+tc.name, "test", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))
			if tc.engaged {
				p.markJevThreadEngaged("oc_test", tc.rootID)
			}
			messages := make(chan *core.Message, 1)
			p.handler = func(_ core.Platform, msg *core.Message) { messages <- msg }
			messageID, chatID, chatType, msgType, userID := "om_message", "oc_test", "group", "text", "ou_user"
			if tc.messageType != "" {
				msgType = tc.messageType
			}
			content := `{"text":"please help"}`
			created := strconv.FormatInt(time.Now().UnixMilli(), 10)
			var mentions []*larkim.MentionEvent
			if tc.mentioned {
				mentions = []*larkim.MentionEvent{{Id: &larkim.UserId{OpenId: &p.botOpenID}}}
			}
			if err := p.onMessage(context.Background(), &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
				Sender: &larkim.EventSender{SenderId: &larkim.UserId{OpenId: &userID}},
				Message: &larkim.EventMessage{MessageId: &messageID, ChatId: &chatID, ChatType: &chatType,
					MessageType: &msgType, Content: &content, CreateTime: &created, RootId: &tc.rootID, Mentions: mentions},
			}}); err != nil {
				t.Fatal(err)
			}
			if int(calls.Load()) != tc.wantCalls {
				t.Fatalf("Jev calls = %d, want %d", calls.Load(), tc.wantCalls)
			}
			if !tc.wantRoute {
				select {
				case msg := <-messages:
					t.Fatalf("unexpected routed message: %+v", msg)
				default:
				}
				return
			}
			select {
			case msg := <-messages:
				if msg.SessionKey != tc.wantKey {
					t.Fatalf("session key = %q, want %q", msg.SessionKey, tc.wantKey)
				}
				rc := msg.ReplyCtx.(replyContext)
				if rc.messageID != messageID {
					t.Fatal("lost triggering message")
				}
				if err := p.replyMessage(context.Background(), rc, "text", `{"text":"finished"}`); err != nil {
					t.Fatal(err)
				}
				// Completion uses the original event context, even without @.
				p.AddDoneReaction(rc)
				select {
				case emoji := <-reactions:
					if emoji != "Done" {
						t.Fatalf("reaction=%q", emoji)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("missing Done on Jev trigger")
				}
				if got := p.shouldReplyInThread(rc); got != tc.wantThread {
					t.Fatalf("reply in thread = %v, want %v", got, tc.wantThread)
				}
				if got := p.shouldUseThreadOrReplyAPI(rc); got != tc.wantThread {
					t.Fatalf("reply API = %v, want %v", got, tc.wantThread)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("message was not routed")
			}
		})
	}
}

func TestJevResponseScopeRejectsInvalidValue(t *testing.T) {
	_, err := newPlatform("lark", "https://open.larksuite.com", map[string]any{
		"app_id": "cli_test", "app_secret": "secret", "jev_response_scope": "everywhere",
	})
	if err == nil {
		t.Fatal("invalid Jev response scope must not start")
	}
}

func TestJevChannelPreservesTopicWithIsolationDisabled(t *testing.T) {
	pAny, err := newPlatform("lark", "https://open.larksuite.com", map[string]any{
		"app_id": "cli_test", "app_secret": "secret", "jev_channel_admission": true,
		"jev_channel_chats": "oc_test", "jev_response_scope": "channel",
		"thread_isolation": false, "no_reply_to_trigger": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := pAny.(*interactivePlatform)
	chatType, root, thread := "group", "om_root", "omt_topic"
	for _, tc := range []struct {
		name string
		msg  *larkim.EventMessage
		want string
	}{
		{"top level", &larkim.EventMessage{ChatType: &chatType, MessageId: &root}, "lark:oc_test:root:om_root"},
		{"root", &larkim.EventMessage{ChatType: &chatType, RootId: &root, ThreadId: &thread}, "lark:oc_test:root:om_root"},
		{"thread only", &larkim.EventMessage{ChatType: &chatType, ThreadId: &thread}, "lark:oc_test:thread:omt_topic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := p.makeSessionKey(tc.msg, "oc_test", "ou_user")
			if key != tc.want {
				t.Fatalf("key=%s want=%s", key, tc.want)
			}
			rc := replyContext{messageID: "om_incoming", chatID: "oc_test", chatType: "group", threadID: thread, sessionKey: key}
			if !p.shouldReplyInThread(rc) || !p.shouldUseThreadOrReplyAPI(rc) || p.jevChannelResponse(rc) {
				t.Fatal("topic reply escaped to channel")
			}
			body := p.buildReplyMessageReqBody(rc, "text", `{"text":"answer"}`)
			if body.ReplyInThread == nil || !*body.ReplyInThread {
				t.Fatal("outbound API request must target topic")
			}
			if got := p.sessionKeyFromCardAction("oc_test", "ou_user", map[string]any{"session_key": key}); got != key {
				t.Fatal("card action lost topic")
			}
		})
	}
}
