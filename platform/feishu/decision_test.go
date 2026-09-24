package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	p.SetDecisionHandler(func(id, user, option, comment, msg string, fields ...map[string]any) (*core.Decision, error) {
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
	p.SetDecisionHandler(func(string, string, string, string, string, ...map[string]any) (*core.Decision, error) {
		return nil, errors.New("disk failure")
	})
	resp, handled = p.handleDecisionAction(event)
	if !handled || resp.Toast.Type != "error" || resp.Card != nil {
		t.Fatal("false success after persistence failure")
	}
	p.SetDecisionHandler(func(string, string, string, string, string, ...map[string]any) (*core.Decision, error) {
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

// Exercise the actual SDK decoder with values taken from the generated button,
// instead of inventing a callback independently of the card we send.
func TestDecisionCardV2RoundTripThroughSDK(t *testing.T) {
	v := &core.Decision{ID: "r2", Status: "answered", Spec: core.DecisionSpec{Title: "确认方案", Markdown: "**方案 A**", AllowComment: true, Options: []core.DecisionOption{{ID: "approve", Label: "同意"}}}, OptionID: "approve", Comment: "按方案执行"}
	raw, _ := json.Marshal(decisionCard(v, false))
	var card map[string]any
	_ = json.Unmarshal(raw, &card)
	if card["schema"] != "2.0" {
		t.Fatal("form cards must use explicit Card 2.0 callbacks")
	}
	elems := card["body"].(map[string]any)["elements"].([]any)
	form := elems[1].(map[string]any)["elements"].([]any)
	input := form[0].(map[string]any)
	if input["required"] != false {
		t.Fatal("comment should be optional")
	}
	columns := form[1].(map[string]any)["columns"].([]any)
	button := columns[0].(map[string]any)["elements"].([]any)[0].(map[string]any)
	behaviors, ok := button["behaviors"].([]any)
	if !ok || len(behaviors) != 1 {
		t.Fatal("button has no callback behavior")
	}
	behavior := behaviors[0].(map[string]any)
	if behavior["type"] != "callback" {
		t.Fatal("button is not a callback")
	}
	p := &Platform{platformName: "lark"}
	called := 0
	p.SetDecisionHandler(func(id, user, option, comment, msg string, fields ...map[string]any) (*core.Decision, error) {
		called++
		if id != "r2" || user != "ou_user" || option != "approve" || comment != "按方案执行" || msg != "om_card" {
			t.Fatal("callback was not decoded correctly")
		}
		return v, nil
	})
	handler := dispatcher.NewEventDispatcher("", "").OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
		return p.onCardAction(event)
	})
	payload, _ := json.Marshal(map[string]any{"schema": "2.0", "header": map[string]any{"event_id": "click-test", "event_type": "card.action.trigger", "app_id": "cli_test", "tenant_key": "test"}, "event": map[string]any{"operator": map[string]any{"open_id": "ou_user"}, "context": map[string]any{"open_message_id": "om_card"}, "action": map[string]any{"tag": "button", "value": behavior["value"], "form_value": map[string]any{"comment": "按方案执行"}}}})
	response, err := handler.Do(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(response)
	if called != 1 || !strings.Contains(string(out), `"schema":"2.0"`) || !strings.Contains(string(out), "✓") || strings.Contains(string(out), "decision:submit") {
		t.Fatalf("missing immediate non-interactive receipt: %s", out)
	}
}

func TestDecisionCallbackDoesNotWaitForReceiptPatch(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			_, _ = w.Write([]byte(`{"code":0,"expire":7200,"tenant_access_token":"test"}`))
			return
		}
		if r.Method != http.MethodPatch || r.URL.Path != "/open-apis/im/v1/messages/om_card" {
			t.Errorf("wrong patch target %s %s", r.Method, r.URL.Path)
		}
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer srv.Close()
	defer close(release)
	p := &Platform{platformName: "lark", client: lark.NewClient("decision-patch-test", "secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))}
	p.SetDecisionHandler(func(string, string, string, string, string, ...map[string]any) (*core.Decision, error) {
		return &core.Decision{Spec: core.DecisionSpec{Title: "确认", Markdown: "内容", Options: []core.DecisionOption{{ID: "yes", Label: "同意"}}}, OptionID: "yes"}, nil
	})
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "ou_user"}, Context: &callback.Context{OpenMessageID: "om_card"}, Action: &callback.CallBackAction{Value: map[string]any{"action": "decision:submit", "request_id": "r1", "option_id": "yes"}}}}
	before := time.Now()
	resp, handled := p.handleDecisionAction(event)
	if !handled || resp.Card == nil || time.Since(before) > time.Second {
		t.Fatal("callback waited for network instead of returning receipt")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("global receipt PATCH was not sent")
	}
}

func TestDecisionFormFieldsAndCancel(t *testing.T) {
	v := &core.Decision{ID: "form", Spec: core.DecisionSpec{Title: "Build", Markdown: "Configure", Fields: []core.DecisionField{
		{ID: "branch", Label: "Branch", Type: "text", Required: true, MaxLength: 100, Default: "develop"},
		{ID: "env", Label: "Environment", Type: "select", Options: []core.DecisionOption{{ID: "uat", Label: "UAT"}}, Default: "uat"},
		{ID: "platform", Label: "Platforms", Type: "multiselect", Options: []core.DecisionOption{{ID: "ipa", Label: "IPA"}}},
	}, Options: []core.DecisionOption{{ID: "build", Label: "Build"}, {ID: "cancel", Label: "Cancel", Cancel: true}}}}
	card := decisionCard(v, false)
	raw, _ := json.Marshal(card)
	for _, part := range []string{`"tag":"select_static"`, `"tag":"multi_select_static"`, `"default_value":"develop"`} {
		if !strings.Contains(string(raw), part) {
			t.Fatalf("missing %s: %s", part, raw)
		}
	}
	elems := card["body"].(map[string]any)["elements"].([]map[string]any)
	cancel := elems[len(elems)-1]
	if cancel["tag"] != "button" || cancel["form_action_type"] != nil {
		t.Fatal("cancel must be outside validated form")
	}
	p := &Platform{}
	p.SetDecisionHandler(func(id, user, option, comment, msg string, fields ...map[string]any) (*core.Decision, error) {
		if len(fields) != 1 || fields[0]["branch"] != "develop" || fields[0]["env"] != "uat" {
			t.Fatal("form values lost")
		}
		v.Values = fields[0]
		v.OptionID = option
		return v, nil
	})
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "user"}, Context: &callback.Context{OpenMessageID: "original"}, Action: &callback.CallBackAction{Value: map[string]any{"action": "decision:submit", "request_id": "form", "option_id": "build"}, FormValue: map[string]any{"branch": "develop", "env": "uat", "platform": []any{"ipa"}}}}}
	response, handled := p.handleDecisionAction(event)
	if !handled || response.Card == nil {
		t.Fatal("no inline receipt")
	}
	out, _ := json.Marshal(response.Card)
	if !strings.Contains(string(out), "develop") || strings.Contains(string(out), "decision:submit") {
		t.Fatalf("bad receipt %s", out)
	}
}
