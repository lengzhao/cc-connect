package feishu

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestHandleNexWorkItemCardAction_ReturnsImmediatelyAndRecordsAsync(t *testing.T) {
	var received atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if r.Header.Get("X-LTS-API-Key") != "test-key" {
			t.Errorf("api key = %q", r.Header.Get("X-LTS-API-Key"))
		}
		received.Store(true)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","cardPatches":[{"messageId":"om_card_1","card":{"schema":"2.0"}}]}`))
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
					"action":      "decide",
					"optionId":    "approve",
					"label":       "同意",
					"title":       "审批",
					"type":        "decision",
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
	if resp.Card == nil || resp.Card.Type != "raw" {
		t.Fatalf("expected immediate raw card, got %#v", resp.Card)
	}
	card, ok := resp.Card.Data.(map[string]any)
	if !ok || card["schema"] != "2.0" {
		t.Fatalf("card data = %#v", resp.Card.Data)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !received.Load() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for async LTS callback")
		}
		time.Sleep(10 * time.Millisecond)
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

func TestIsNexCallbackValue(t *testing.T) {
	if !isNexCallbackValue(map[string]any{"nexCallback": true}) {
		t.Fatal("expected true bool")
	}
	if !isNexCallbackValue(map[string]any{"nexCallback": "true"}) {
		t.Fatal("expected true string")
	}
	if isNexCallbackValue(map[string]any{"action": "approve"}) {
		t.Fatal("expected false")
	}
}
