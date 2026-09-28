package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

func TestNewPlatformParsesJevOptions(t *testing.T) {
	p, err := newPlatform("feishu", "https://open.feishu.cn", map[string]any{
		"app_id":                   "cli_test",
		"app_secret":               "sec",
		"require_mention":          true,
		"jev_channel_admission":     true,
		"jev_admission_url":         "http://127.0.0.1:8020/jev/admit",
		"jev_channel_chats":        "oc_test",
		"jev_admission_timeout_ms":  5000,
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
