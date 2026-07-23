package askuser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestParseToolArguments_RichFields(t *testing.T) {
	raw := json.RawMessage(`{
		"question":"Which wallet?",
		"event":"connect_account",
		"allow_custom_input":true,
		"options":[
			{"label":"metamask","value":"metamask","description":"Use metamask","tag":{"text":"Recommended","variant":"recommend"}},
			{"label":"imToken","value":"imToken"}
		]
	}`)
	q, err := ParseToolArguments(raw)
	if err != nil {
		t.Fatal(err)
	}
	if q.Question != "Which wallet?" || q.Event != "connect_account" || !q.AllowCustomInput {
		t.Fatalf("%+v", q)
	}
	if len(q.Options) != 2 || q.Options[0].Value != "metamask" || q.Options[0].TagVariant != "recommend" {
		t.Fatalf("options=%+v", q.Options)
	}
	if q.Options[0].Tag != "Recommended" {
		t.Fatalf("tag text=%q", q.Options[0].Tag)
	}
}

func TestNormalizeEvent_AllowsEmptyAndDropsUnknown(t *testing.T) {
	if got := NormalizeEvent("connect_account"); got != EventConnectAccount {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeEvent(""); got != "" {
		t.Fatalf("empty got %q", got)
	}
	if got := NormalizeEvent("unknown_flow"); got != "" {
		t.Fatalf("unknown got %q", got)
	}
	q, err := ParseToolArguments(json.RawMessage(`{
		"question":"Q","event":"not_a_real_event",
		"options":[{"label":"A"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if q.Event != "" {
		t.Fatalf("unmatched event must clear, got %q", q.Event)
	}
}

func TestNormalizeTagVariant(t *testing.T) {
	if got := NormalizeTagVariant("recommend"); got != TagVariantRecommend {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeTagVariant("WARNING"); got != TagVariantWarning {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeTagVariant("blue"); got != "" {
		t.Fatalf("unknown got %q", got)
	}
}

func TestToolDescriptor_HasDescriptionsAndEnums(t *testing.T) {
	desc := toolDescriptor()
	schema, _ := desc["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	event, _ := props["event"].(map[string]any)
	if !strings.Contains(fmt.Sprint(event["description"]), "引导应用进行页面跳转") {
		t.Fatalf("event description=%v", event["description"])
	}
	options, _ := props["options"].(map[string]any)
	items, _ := options["items"].(map[string]any)
	oprops, _ := items["properties"].(map[string]any)
	tag, _ := oprops["tag"].(map[string]any)
	tprops, _ := tag["properties"].(map[string]any)
	variant, _ := tprops["variant"].(map[string]any)
	enum, _ := variant["enum"].([]any)
	want := map[string]bool{"recommend": true, "keep": true, "default": true, "warning": true}
	if len(enum) != 4 {
		t.Fatalf("variant enum=%v", enum)
	}
	for _, v := range enum {
		s, _ := v.(string)
		if !want[s] {
			t.Fatalf("unexpected variant %q", s)
		}
	}
	if !strings.Contains(fmt.Sprint(variant["description"]), "推荐（绿）") {
		t.Fatalf("variant description=%v", variant["description"])
	}
}

func TestWriteMCPConfig_SessionHeader(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/mcp.json"
	if err := WriteMCPConfig(path, "http://127.0.0.1:9/mcp", "proj:chat:u1"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(core.SessionKeyHeader)) || !bytes.Contains(b, []byte("proj:chat:u1")) {
		t.Fatalf("config=%s", b)
	}
	if !bytes.Contains(b, []byte(`"timeout": 3600000`)) {
		t.Fatalf("missing timeout: %s", b)
	}
}

func TestServer_ToolsCallAsk(t *testing.T) {
	hub := core.NewAskUserHub()
	srv, err := Start(hub)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close(context.Background())

	hub.Bind("sess-1", askEmitterFunc(func(e core.Event) error {
		go func() {
			time.Sleep(20 * time.Millisecond)
			hub.Complete(e.RequestID, map[int]string{0: "metamask"}, map[int]string{0: "metamask"})
		}()
		return nil
	}))

	// initialize
	postRPC(t, srv.MCPURL(), "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{},
	})

	list := postRPC(t, srv.MCPURL(), "sess-1", map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{},
	})
	if !bytes.Contains(list, []byte(core.ToolCCConnectAskUser)) {
		t.Fatalf("tools/list=%s", list)
	}

	call := postRPC(t, srv.MCPURL(), "sess-1", map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name": core.ToolCCConnectAskUser,
			"arguments": map[string]any{
				"question":           "Which wallet?",
				"event":              "connect_account",
				"allow_custom_input": true,
				"options": []any{
					map[string]any{"label": "metamask", "value": "metamask"},
				},
			},
		},
	})
	if !bytes.Contains(call, []byte("metamask")) {
		t.Fatalf("tools/call=%s", call)
	}
}

func postRPC(t *testing.T, url, sessionKey string, body any) []byte {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionKey != "" {
		req.Header.Set(core.SessionKeyHeader, sessionKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, buf.String())
	}
	return buf.Bytes()
}

type askEmitterFunc func(core.Event) error

func (f askEmitterFunc) EmitAskUser(e core.Event) error { return f(e) }
