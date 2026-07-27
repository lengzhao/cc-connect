package chatapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuestionNotifyBody_MinimalFields(t *testing.T) {
	body := questionNotifyBody(&runState{
		id:             "run_abc",
		conversationID: "conv_1",
		messageID:      "conv_1:0",
		user:           "user_001",
		channelKey:     "default_channel",
	})
	want := map[string]string{
		"conversation_id": "conv_1",
		"message_id":      "conv_1:0",
		"run_id":          "run_abc",
		"user_id":         "user_001",
		"channel":         "default_channel",
	}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("body[%q]=%q want %q", k, body[k], v)
		}
	}
	if len(body) != len(want) {
		t.Fatalf("body=%v want only %v", body, want)
	}
}

func TestApplyQuestionNotifyHeaders_CustomAndDefaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://example.com/notify", nil)
	applyQuestionNotifyHeaders(req, map[string]string{
		"access_token":     "tok",
		"origin_channel":   "BACKEND",
		"client_platform":  "WEB",
		"X-Chat-API-Event": "custom-event",
	}, "sec")

	if got := req.Header.Get("access_token"); got != "tok" {
		t.Fatalf("access_token=%q", got)
	}
	if got := req.Header.Get("origin_channel"); got != "BACKEND" {
		t.Fatalf("origin_channel=%q", got)
	}
	if got := req.Header.Get("client_platform"); got != "WEB" {
		t.Fatalf("client_platform=%q", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type=%q", got)
	}
	if got := req.Header.Get("X-Chat-API-Notify-Secret"); got != "sec" {
		t.Fatalf("secret=%q", got)
	}
	if got := req.Header.Get("X-Chat-API-Event"); got != "custom-event" {
		t.Fatalf("event=%q", got)
	}
}

func TestApplyQuestionNotifyHeaders_SecretNotOverrideCustom(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://example.com/notify", nil)
	applyQuestionNotifyHeaders(req, map[string]string{
		"X-Chat-API-Notify-Secret": "from-config",
	}, "sec")
	if got := req.Header.Get("X-Chat-API-Notify-Secret"); got != "from-config" {
		t.Fatalf("secret=%q want from-config", got)
	}
}
