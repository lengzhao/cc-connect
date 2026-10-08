package feishu

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestOriginalSenderTypeReachesProcessingHook(t *testing.T) {
	for _, typ := range []string{"user", "app", ""} {
		t.Run(typ, func(t *testing.T) {
			base, err := newPlatform("lark", "https://open.larksuite.com", map[string]any{"app_id": "test", "app_secret": "test", "require_mention": false})
			if err != nil {
				t.Fatal(err)
			}
			p := base.(*interactivePlatform)
			p.botOpenID = "ou_receiver"
			p.chatNameCache.Store("oc_test", "Test")
			p.userNameCache.Store("ou_sender", feishuUserInfo{name: "Sender", email: "sender@example.com"})
			received := make(chan *core.Message, 1)
			p.handler = func(_ core.Platform, m *core.Message) { received <- m }
			var senderType *string
			if typ != "" {
				senderType = stringPtr(typ)
			}
			e := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
				Sender:  &larkim.EventSender{SenderId: &larkim.UserId{OpenId: stringPtr("ou_sender")}, SenderType: senderType},
				Message: &larkim.EventMessage{MessageId: stringPtr("om_new"), ChatId: stringPtr("oc_test"), ChatType: stringPtr("p2p"), MessageType: stringPtr("text"), Content: stringPtr(`{"text":"hello"}`)},
			}}
			if err := p.onMessage(context.Background(), e); err != nil {
				t.Fatal(err)
			}
			select {
			case msg := <-received:
				for _, event := range []core.HookEventType{core.HookEventMessageReceived, core.HookEventMessageProcessing} {
					hook := core.HookEventFromMessage("test", p, msg, event, "")
					got, exists := hook.Context["sender_type"]
					if (typ == "" && exists) || (typ != "" && got != typ) {
						t.Fatalf("type=%q context=%v", typ, hook.Context)
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("not dispatched")
			}
		})
	}
}

func TestImageBatchPreservesSenderType(t *testing.T) {
	p := &Platform{platformName: "lark"}
	received := make(chan *core.Message, 1)
	p.handler = func(_ core.Platform, m *core.Message) { received <- m }
	p.dispatchImageBatchEntry(&imageBatchEntry{sessionKey: "lark:oc_test:ou_sender", userID: "ou_sender", rctx: replyContext{chatID: "oc_test", senderType: "app"}, images: []core.ImageAttachment{{MimeType: "image/png", Data: []byte("fake")}}, messageIDs: []string{"om_img"}})
	select {
	case m := <-received:
		if p.HookContext(m.ReplyCtx).Context["sender_type"] != "app" {
			t.Fatal("image batch lost type")
		}
	case <-time.After(time.Second):
		t.Fatal("missing batch")
	}
}

func TestCatchupPreservesSenderType(t *testing.T) {
	base, err := newPlatform("lark", "https://open.larksuite.com", map[string]any{"app_id": "test", "app_secret": "test"})
	if err != nil {
		t.Fatal(err)
	}
	p := base.(*interactivePlatform)
	p.botOpenID = "ou_receiver"
	p.userNameCache.Store("ou_sender", feishuUserInfo{name: "Sender"})
	p.chatNameCache.Store("oc_test", "Test")
	got := make(chan *core.Message, 1)
	p.handler = func(_ core.Platform, m *core.Message) { got <- m }
	p.injectCatchupMessage(context.Background(), &larkim.Message{MessageId: stringPtr("om_catchup"), MsgType: stringPtr("text"), Sender: &larkim.Sender{Id: stringPtr("ou_sender"), SenderType: stringPtr("app")}, Body: &larkim.MessageBody{Content: stringPtr(`{"text":"hello"}`)}}, "oc_test", time.Now().UnixMilli())
	select {
	case m := <-got:
		if p.HookContext(m.ReplyCtx).Context["sender_type"] != "app" {
			t.Fatal("catchup lost sender type")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no catchup dispatch")
	}
}
