package feishu

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestLocateDecisionMessageReadsExactPlatformMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, body, thread string
		fail               bool
	}{
		{"topic", `{"code":0,"data":{"items":[{"message_id":"om_card","chat_id":"oc_group","thread_id":"omt_real","root_id":"om_root","parent_id":"om_parent"}]}}`, "omt_real", false},
		{"no_topic", `{"code":0,"data":{"items":[{"message_id":"om_card","chat_id":"oc_group"}]}}`, "", false},
		{"wrong_message", `{"code":0,"data":{"items":[{"message_id":"om_other","chat_id":"oc_group","thread_id":"omt_wrong"}]}}`, "", true},
		{"permission_denied", `{"code":230001,"msg":"denied"}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					fmt.Fprint(w, `{"code":0,"expire":7200,"tenant_access_token":"test"}`)
					return
				}
				if r.URL.Path != "/open-apis/im/v1/messages/om_card" || r.Method != "GET" {
					t.Errorf("unexpected API: %s %s", r.Method, r.URL.Path)
				}
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			p := &Platform{platformName: "lark", client: lark.NewClient("location-"+tc.name, "secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))}
			got, err := p.LocateDecisionMessage(context.Background(), "om_card")
			if (err != nil) != tc.fail || got.ThreadID != tc.thread {
				t.Fatalf("location=%+v err=%v", got, err)
			}
			if !tc.fail && (got.ChatID != "oc_group" || got.MessageID != "om_card" || got.Platform != "lark") {
				t.Fatalf("wrong identity: %+v", got)
			}
			if _, err := p.LocateDecisionMessage(context.Background(), "decision:synthetic"); err == nil {
				t.Fatal("queried synthetic callback ID")
			}
		})
	}
}
