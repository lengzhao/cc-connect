package feishu

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestHandleNexWorkItemCardAction_ForwardsAndReturnsToast(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		if r.Header.Get("X-LTS-API-Key") != "test-key" {
			t.Errorf("api key = %q", r.Header.Get("X-LTS-API-Key"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	p := &Platform{
		platformName:              "lark",
		ltsWorkItemCallbackURL:      server.URL + "/api/lark/work-items/callback",
		ltsWorkItemCallbackAPIKey:   "test-key",
		ltsWorkItemCallbackHTTP:     server.Client(),
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

	deadline := time.Now().Add(2 * time.Second)
	for received == nil {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for LTS callback")
		}
		time.Sleep(10 * time.Millisecond)
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
