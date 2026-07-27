package chatapi

import "testing"

func TestStringStringMapOption(t *testing.T) {
	got := stringStringMapOption(map[string]any{
		"question_notify_headers": map[string]string{
			"access_token":    "tok",
			"origin_channel":  "BACKEND",
			"client_platform": "WEB",
		},
	}, "question_notify_headers")
	if got["access_token"] != "tok" || got["origin_channel"] != "BACKEND" || got["client_platform"] != "WEB" {
		t.Fatalf("got=%v", got)
	}
}
