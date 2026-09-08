package feishu

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestHandleNexWorkItemCardAction_ForwardsAndReturnsToastAndCard(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		if r.Header.Get("X-LTS-API-Key") != "test-key" {
			t.Errorf("api key = %q", r.Header.Get("X-LTS-API-Key"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","cardPatches":[{"messageId":"om_card_1","card":{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"done"}}}}]}`))
	}))
	defer server.Close()

	p := &Platform{
		platformName:            "lark",
		ltsWorkItemCallbackURL:    server.URL + "/api/lark/work-items/callback",
		ltsWorkItemCallbackAPIKey: "test-key",
		ltsWorkItemCallbackHTTP:   server.Client(),
	}
	resp, handled := p.handleNexWorkItemCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Action: &callback.CallBackAction{
				Value: map[string]any{
					"nexCallback": true,
					"tenantId":    "nex-workbench:alice",
					"workItemId":  "wi-1",
					"action":      "approve",
					"optionId":    "approve",
					"label":       "通过",
					"title":       "审批",
					"type":        "review",
					"inboxUrl":    "https://wb/inbox/wi-1",
					"answer":      map[string]any{"optionIds": []string{"approve"}},
				},
				FormValue: map[string]any{"nex_comment": " looks good "},
			},
			Context: &callback.Context{OpenMessageID: "om_card_1"},
		},
	})
	if !handled || resp == nil || resp.Toast == nil {
		t.Fatalf("expected handled toast response, got handled=%v resp=%#v", handled, resp)
	}
	if resp.Toast.Content == "" {
		t.Fatalf("expected toast content")
	}
	if resp.Card == nil || resp.Card.Type != "raw" {
		t.Fatalf("expected raw card in callback response, got %#v", resp.Card)
	}
	card, ok := resp.Card.Data.(map[string]any)
	if !ok || card["schema"] != "2.0" {
		t.Fatalf("card data = %#v", resp.Card.Data)
	}
	if received["workItemId"] != "wi-1" || received["messageId"] != "om_card_1" {
		t.Fatalf("payload = %#v", received)
	}
	if received["comment"] != "looks good" {
		t.Fatalf("comment = %#v", received["comment"])
	}
}

func TestHandleNexWorkItemCardAction_IgnoresNonNexCallback(t *testing.T) {
	p := &Platform{ltsWorkItemCallbackURL: "http://example.invalid"}
	resp, handled := p.handleNexWorkItemCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Action: &callback.CallBackAction{Value: map[string]any{"action": "approve"}},
		},
	})
	if handled || resp != nil {
		t.Fatalf("expected ignore, got handled=%v resp=%#v", handled, resp)
	}
}

func TestPatchCardForMessage(t *testing.T) {
	card := patchCardForMessage([]nexWorkItemCardPatch{
		{MessageID: "om_1", Card: map[string]any{"schema": "2.0"}},
	}, "om_1")
	if card == nil || card["schema"] != "2.0" {
		t.Fatalf("card = %#v", card)
	}
}
