package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestDecisionCardFormUsesHostIDsAndPreservesMarkdown(t *testing.T) {
	v := &core.Decision{ID: "request1", Spec: core.DecisionSpec{Title: "请确认", Markdown: "**方案** [详情](https://example.com)", AllowComment: true, Options: []core.DecisionOption{{ID: "approve", Label: "确认"}, {ID: "revise", Label: "修改"}}}}
	card := decisionCard(v, false)
	data, _ := json.Marshal(card)
	for _, want := range []string{"form_action_type", "submit", "decision:submit", "request1", "option_id", "comment", "https://example.com"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s in %s", want, data)
		}
	}
	if strings.Contains(string(data), "session_key") {
		t.Fatal("card exposes origin routing")
	}
	v.OptionID = "approve"
	v.Comment = "yes"
	data, _ = json.Marshal(decisionCard(v, true))
	if strings.Contains(string(data), "decision:submit") {
		t.Fatal("answered card still actionable")
	}
}
func TestDecisionLongConnectionCallbackPersistsBeforeAcknowledgement(t *testing.T) {
	p := &Platform{}
	saved := false
	p.SetDecisionHandler(func(id, user, option, comment, msg string) (*core.Decision, error) {
		if id != "r1" || user != "ou_user" || option != "yes" || comment != "notes" || msg != "om_card" {
			t.Fatalf("wrong callback values: %s %s %s %s %s", id, user, option, comment, msg)
		}
		saved = true
		return &core.Decision{Spec: core.DecisionSpec{Title: "请确认", Options: []core.DecisionOption{{ID: "yes", Label: "确认"}}}, OptionID: option, Comment: comment}, nil
	})
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "ou_user"}, Context: &callback.Context{OpenMessageID: "om_card"}, Action: &callback.CallBackAction{Value: map[string]any{"action": "decision:submit", "request_id": "r1", "option_id": "yes"}, FormValue: map[string]any{"comment": "notes"}}}}
	resp, handled := p.handleDecisionAction(event)
	if !handled || !saved || resp.Toast.Type != "success" || resp.Card == nil {
		t.Fatalf("bad ack: %+v", resp)
	}
	p.SetDecisionHandler(func(string, string, string, string, string) (*core.Decision, error) {
		return nil, errors.New("disk failure")
	})
	resp, handled = p.handleDecisionAction(event)
	if !handled || resp.Toast.Type != "error" || resp.Card != nil {
		t.Fatal("false success after persistence failure")
	}
	p.SetDecisionHandler(func(string, string, string, string, string) (*core.Decision, error) {
		return nil, core.ErrDecisionNotFound
	})
	if _, handled = p.handleDecisionAction(event); handled {
		t.Fatal("swallowed another project's callback")
	}
}
func TestDecisionSendUsesAppScopedIdentityAndThreadReply(t *testing.T) {
	var create, reply bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"expire":7200,"tenant_access_token":"test"}`))
		case "/open-apis/contact/v3/users/batch_get_id":
			if r.URL.Query().Get("user_id_type") != "open_id" {
				t.Error("wrong identity type")
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"user_list":[{"user_id":"ou_recipient"}]}}`))
		case "/open-apis/im/v1/messages", "/open-apis/im/v1/messages/om_root/reply":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["uuid"] != "stable-id" {
				t.Error("missing send idempotency")
			}
			if strings.HasSuffix(r.URL.Path, "/reply") {
				reply = true
				if body["reply_in_thread"] != true {
					t.Error("lost thread")
				}
			} else {
				create = true
				if body["receive_id"] != "ou_recipient" || r.URL.Query().Get("receive_id_type") != "open_id" {
					t.Error("wrong recipient")
				}
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_sent"}}`))
		default:
			t.Errorf("unexpected API %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	p := &Platform{platformName: "lark", threadIsolation: true, client: lark.NewClient("decision-test-app", "secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))}
	id, err := p.ResolveDecisionRecipient(context.Background(), "user@example.com")
	if err != nil || id != "ou_recipient" {
		t.Fatalf("%s %v", id, err)
	}
	if _, err = p.ResolveDecisionRecipient(context.Background(), "ou_bad@ambr.io"); err == nil {
		t.Fatal("accepted open_id disguised as email")
	}
	v := &core.Decision{ID: "stable-id", RecipientID: id, Spec: core.DecisionSpec{Title: "Test", Markdown: "body", Options: []core.DecisionOption{{ID: "yes", Label: "yes"}}}}
	if _, err = p.SendDecision(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	v.DeliverySessionKey = "lark:oc_chat:thread:om_root"
	if _, err = p.SendDecision(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if !create || !reply {
		t.Fatal("did not test both direct and thread delivery")
	}
}
