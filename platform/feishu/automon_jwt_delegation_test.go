package feishu

import (
	"context"
	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"testing"
	"time"
)

func TestAutomonJWTDelegationsValidation(t *testing.T) {
	for _, raw := range []any{nil, []string{}, []string{"ou_bot=owner@example.com"}, []any{"ou_bot=owner@example.com"}} {
		if _, err := parseAutomonJWTDelegations(raw); err != nil {
			t.Fatalf("valid config rejected: %v", err)
		}
	}
	for _, raw := range []any{"ou_bot=owner@example.com", []any{42}, []string{"*=owner@example.com"}, []string{"ou_=owner@example.com"}, []string{"ou_bot=bad"}, []string{"ou_bot=Owner <owner@example.com>"}, []string{"ou_bot=a@example.com", "ou_bot=b@example.com"}, []string{"ou_bot=a@example.com\nInjected"}} {
		if _, err := parseAutomonJWTDelegations(raw); err == nil {
			t.Errorf("invalid config accepted: %#v", raw)
		}
	}
}

func TestBotSenderEmailDelegationWithoutContactLookup(t *testing.T) {
	// A nil API client ensures a mapped bot never needs a Contact API lookup.
	p := &Platform{automonJWTDelegations: map[string]string{"ou_bot": "owner@example.com"}}
	for _, enabled := range []bool{false, true} {
		p.includeUserEmail = enabled
		name, email := p.resolveUserNameAndEmail("ou_bot")
		if name != "ou_bot" || email != "owner@example.com" {
			t.Fatalf("delegation lost: %q %q", name, email)
		}
	}
	p.userNameCache.Store("ou_other", feishuUserInfo{name: "Other", email: "other@example.com"})
	name, email := p.resolveUserNameAndEmail("ou_other")
	if name != "Other" || email != "other@example.com" {
		t.Fatalf("unmapped user changed: %q %q", name, email)
	}
	p.includeUserEmail = false
	_, email = p.resolveUserNameAndEmail("ou_other")
	if email != "" {
		t.Fatal("unmapped user bypassed include_user_email")
	}
}

func TestNewRejectsInvalidAutomonJWTDelegations(t *testing.T) {
	_, err := New(map[string]any{"app_id": "test", "app_secret": "test", "automon_jwt_delegations": []any{"*=owner@example.com"}})
	if err == nil {
		t.Fatal("invalid delegation must reject platform initialization")
	}
}

func TestAutomonJWTDelegationsTOML(t *testing.T) {
	var cfg struct{ Options map[string]any }
	if _, err := toml.Decode(`[options]
automon_jwt_delegations = ["ou_bot=owner@example.com"]
`, &cfg); err != nil {
		t.Fatal(err)
	}
	mappings, err := parseAutomonJWTDelegations(cfg.Options["automon_jwt_delegations"])
	if err != nil || mappings["ou_bot"] != "owner@example.com" {
		t.Fatalf("TOML mapping lost: %v %v", mappings, err)
	}
}

func TestAutomonJWTDelegationBotMentionToHook(t *testing.T) {
	platform, err := newPlatform("lark", "https://open.larksuite.com", map[string]any{
		"app_id": "cli_test", "app_secret": "test", "require_mention": true,
		"allow_from": "ou_source_bot", "allow_chat": "oc_delegation",
		"automon_jwt_delegations": []any{"ou_source_bot=owner@ambr.io"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := platform.(*interactivePlatform)
	p.botOpenID = "ou_receiver_bot"
	p.chatNameCache.Store("oc_delegation", "Delegation test")
	got := make(chan *core.Message, 1)
	p.handler = func(_ core.Platform, m *core.Message) { got <- m }
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender:  &larkim.EventSender{SenderId: &larkim.UserId{OpenId: stringPtr("ou_source_bot")}, SenderType: stringPtr("app")},
		Message: &larkim.EventMessage{MessageId: stringPtr("om_delegation"), ChatId: stringPtr("oc_delegation"), ChatType: stringPtr("group"), MessageType: stringPtr("text"), Content: stringPtr(`{"text":"@_user_1 process ticket"}`), Mentions: []*larkim.MentionEvent{{Key: stringPtr("@_user_1"), Id: &larkim.UserId{OpenId: stringPtr("ou_receiver_bot")}, Name: stringPtr("Receiver")}}},
	}}
	if err := p.onMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m.UserID != "ou_source_bot" || m.UserEmail != "owner@ambr.io" || !m.BotMentioned {
			t.Fatalf("wrong identity: %+v", m)
		}
		hook := core.HookEventFromMessage("automon", p, m, core.HookEventMessageReceived, "")
		if hook.UserID != "ou_source_bot" || hook.UserEmail != "owner@ambr.io" {
			t.Fatalf("hook lost delegation: %+v", hook)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bot mention was not dispatched")
	}
	// A configured sender without @ still obeys require_mention.
	event.Event.Message.MessageId = stringPtr("om_no_mention")
	event.Event.Message.Mentions = nil
	if err := p.onMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
		t.Fatal("delegation bypassed mention guard")
	case <-time.After(100 * time.Millisecond):
	}
}
