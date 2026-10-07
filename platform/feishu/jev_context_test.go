package feishu

import (
	"context"
	"encoding/json"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJevThreadContextUsesOnlyTopicHistory(t *testing.T) {
	listCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			w.Write([]byte(`{"code":0,"expire":7200,"tenant_access_token":"test"}`))
		case "/open-apis/im/v1/messages/om_root":
			w.Write([]byte(`{"code":0,"data":{"items":[{"message_id":"om_root","thread_id":"omt_topic","chat_id":"oc_chat","msg_type":"text","create_time":"100","body":{"content":"{\"text\":\"Please help\"}"}}]}}`))
		case "/open-apis/im/v1/messages":
			listCalls++
			if r.URL.Query().Get("container_id_type") != "thread" || r.URL.Query().Get("container_id") != "omt_topic" || r.URL.Query().Get("page_size") != "8" {
				t.Errorf("unexpected history query: %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"code":0,"data":{"items":[{"message_id":"om_bot","chat_id":"oc_chat","msg_type":"text","create_time":"200","sender":{"id":"ou_bot","sender_type":"app"},"body":{"content":"{\"text\":\"Which date?\"}"}},{"message_id":"om_now","msg_type":"text","body":{"content":"{\"text\":\"current excluded\"}"}}]}}`))
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &Platform{client: lark.NewClient("jev-context-test", "test", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())), botOpenID: "ou_bot"}
	got := p.jevThreadContext(context.Background(), jevAdmitRequest{ChannelID: "oc_chat", RootID: "om_root", MessageID: "om_now", CreatedAtMS: 300})
	if listCalls != 1 || !strings.Contains(got, "Please help") || !strings.Contains(got, "bot:ou_bot: Which date?") || strings.Contains(got, "excluded") {
		t.Fatalf("bad topic context: %q", got)
	}
}

func TestJevContextFiltersAndBounds(t *testing.T) {
	var items []*larkim.Message
	raw := `[{"message_id":"a","chat_id":"oc_chat","msg_type":"text","create_time":"100","body":{"content":"{\"text\":\"older\"}"}},
 {"message_id":"b","chat_id":"oc_other","msg_type":"text","body":{"content":"{\"text\":\"other chat\"}"}},
 {"message_id":"c","deleted":true,"msg_type":"text","body":{"content":"{\"text\":\"deleted\"}"}},
 {"message_id":"d","create_time":"900","msg_type":"text","body":{"content":"{\"text\":\"future\"}"}}]`
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatal(err)
	}
	items = append(items, items[0])
	got := formatJevContext(items, jevAdmitRequest{ChannelID: "oc_chat", CreatedAtMS: 200}, "")
	if got != "unknown: older" {
		t.Fatalf("bad filtering %q", got)
	}
	longContent := `{"text":"` + strings.Repeat("中", 700) + `"}`
	items[0].Body.Content = &longContent
	got = formatJevContext(items[:1], jevAdmitRequest{ChannelID: "oc_chat"}, "")
	if len([]rune(got)) > 620 || !strings.HasSuffix(got, "…") {
		t.Fatalf("unbounded context: %d", len([]rune(got)))
	}
}
