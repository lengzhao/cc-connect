package askuser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	if !strings.Contains(fmt.Sprint(event["description"]), "connect_account") {
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

func TestStartUnix_ServesMCP(t *testing.T) {
	hub := core.NewAskUserHub()
	dir := t.TempDir()
	srv, err := StartUnix(hub, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close(context.Background())

	if srv.SocketPath() == "" {
		t.Fatal("expected socket path")
	}
	if srv.MCPURL() != "http://localhost/mcp" {
		t.Fatalf("MCPURL=%q", srv.MCPURL())
	}
	if _, err := os.Stat(srv.SocketPath()); err != nil {
		t.Fatalf("socket missing: %v", err)
	}

	postRPCUnix(t, srv.SocketPath(), "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{},
	})
}

func TestResolveSocketPath_Default(t *testing.T) {
	got, err := ResolveSocketPath("/tmp/cc-connect-test", "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/cc-connect-test", "run", askUserMCPSocket)
	if got != want {
		t.Fatalf("ResolveSocketPath() = %q, want %q", got, want)
	}
}

func TestResolveSocketPath_Configured(t *testing.T) {
	custom := "/tmp/cc-connect-custom-ask.sock"
	got, err := ResolveSocketPath("/tmp/cc-connect-test", custom)
	if err != nil {
		t.Fatal(err)
	}
	if got != custom {
		t.Fatalf("ResolveSocketPath() = %q, want %q", got, custom)
	}
}

func TestStartUnix_UsesConfiguredSocket(t *testing.T) {
	hub := core.NewAskUserHub()
	custom := filepath.Join(t.TempDir(), "x.sock")
	if len(custom) > maxUnixSocketPath {
		custom = "/tmp/cc-connect-configured-ask.sock"
	}
	srv, err := StartUnix(hub, "/tmp/cc-connect-test", custom)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close(context.Background())

	if srv.SocketPath() != custom {
		t.Fatalf("SocketPath() = %q, want %q", srv.SocketPath(), custom)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatalf("configured socket missing: %v", err)
	}

	postRPCUnix(t, custom, "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{},
	})
}

func TestParseClientFlowArguments(t *testing.T) {
	in, err := ParseClientFlowArguments(json.RawMessage(`{"type":"connect_account","description":"绑定新账户"}`))
	if err != nil || in.Type != EventConnectAccount || in.Description != "绑定新账户" {
		t.Fatalf("%+v %v", in, err)
	}

	in, err = ParseClientFlowArguments(json.RawMessage(`{"type":" create_task ","description":" 创建任务 "}`))
	if err != nil || in.Type != EventCreateTask || in.Description != "创建任务" {
		t.Fatalf("trim: %+v %v", in, err)
	}

	_, err = ParseClientFlowArguments(json.RawMessage(`{"type":"account_bind","description":"x"}`))
	if err == nil {
		t.Fatal("account_bind must fail")
	}
	_, err = ParseClientFlowArguments(json.RawMessage(`{"type":"unknown_flow","description":"x"}`))
	if err == nil {
		t.Fatal("unknown type must fail")
	}
	_, err = ParseClientFlowArguments(json.RawMessage(`{"type":"connect_account","description":"  "}`))
	if err == nil {
		t.Fatal("empty description must fail")
	}
	_, err = ParseClientFlowArguments(json.RawMessage(`{"description":"x"}`))
	if err == nil {
		t.Fatal("missing type must fail")
	}
	_, err = ParseClientFlowArguments(json.RawMessage(`{"type":"connect_account"}`))
	if err == nil {
		t.Fatal("missing description must fail")
	}
	_, err = ParseClientFlowArguments(json.RawMessage(`[]`))
	if err == nil {
		t.Fatal("non-object must fail")
	}
	_, err = ParseClientFlowArguments(nil)
	if err == nil {
		t.Fatal("nil args must fail")
	}
}

func TestClientFlowToolDescriptor(t *testing.T) {
	desc := clientFlowToolDescriptor()
	if desc["name"] != core.ToolCCConnectClientFlow {
		t.Fatalf("name=%v", desc["name"])
	}
	if !strings.Contains(fmt.Sprint(desc["description"]), "非阻塞") {
		t.Fatalf("description should clarify non-blocking: %v", desc["description"])
	}
	schema, _ := desc["inputSchema"].(map[string]any)
	gotReq := map[string]bool{}
	switch req := schema["required"].(type) {
	case []string:
		for _, r := range req {
			gotReq[r] = true
		}
	case []any:
		for _, r := range req {
			s, _ := r.(string)
			gotReq[s] = true
		}
	default:
		t.Fatalf("required type %T = %v", schema["required"], schema["required"])
	}
	if !gotReq["type"] || !gotReq["description"] || len(gotReq) != 2 {
		t.Fatalf("required=%v", schema["required"])
	}
	props, _ := schema["properties"].(map[string]any)
	typ, _ := props["type"].(map[string]any)
	enum, _ := typ["enum"].([]any)
	if len(enum) != 4 {
		t.Fatalf("type enum must be exactly 4 values, got %v", enum)
	}
	for _, v := range enum {
		s, _ := v.(string)
		if s == "" {
			t.Fatal("type enum must not include empty string")
		}
		if NormalizeEvent(s) == "" {
			t.Fatalf("unexpected enum value %q", s)
		}
	}
	if _, ok := props["description"].(map[string]any); !ok {
		t.Fatal("description property required")
	}
}

func TestServer_ToolsCallClientFlow(t *testing.T) {
	hub := core.NewAskUserHub()
	srv, err := Start(hub)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close(context.Background())

	var got core.Event
	emitted := make(chan struct{}, 1)
	hub.Bind("sess-cf", askEmitterFunc(func(e core.Event) error {
		got = e
		emitted <- struct{}{}
		return nil
	}))

	postRPC(t, srv.MCPURL(), "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{},
	})

	list := postRPC(t, srv.MCPURL(), "sess-cf", map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{},
	})
	if !bytes.Contains(list, []byte(core.ToolCCConnectAskUser)) {
		t.Fatalf("tools/list missing ask: %s", list)
	}
	if !bytes.Contains(list, []byte(core.ToolCCConnectClientFlow)) {
		t.Fatalf("tools/list missing client_flow: %s", list)
	}

	start := time.Now()
	call := postRPC(t, srv.MCPURL(), "sess-cf", map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name": core.ToolCCConnectClientFlow,
			"arguments": map[string]any{
				"type":        "connect_account",
				"description": "绑定新账户",
			},
		},
	})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("client_flow must return immediately, took %v", elapsed)
	}
	if !bytes.Contains(call, []byte("emitted")) || !bytes.Contains(call, []byte("connect_account")) {
		t.Fatalf("tools/call=%s", call)
	}
	if bytes.Contains(call, []byte(`"isError":true`)) || bytes.Contains(call, []byte(`"isError": true`)) {
		t.Fatalf("unexpected error result: %s", call)
	}

	select {
	case <-emitted:
	case <-time.After(time.Second):
		t.Fatal("expected EventClientFlow emit")
	}
	if got.Type != core.EventClientFlow || got.ToolName != core.ToolCCConnectClientFlow {
		t.Fatalf("event=%+v", got)
	}
	if got.ToolInput != "绑定新账户" {
		t.Fatalf("description=%q", got.ToolInput)
	}
	if got.ToolInputRaw["type"] != "connect_account" {
		t.Fatalf("raw type=%v", got.ToolInputRaw["type"])
	}

	// Qualified MCP name also works.
	callQ := postRPC(t, srv.MCPURL(), "sess-cf", map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name": core.MCPQualifiedClientFlowTool,
			"arguments": map[string]any{
				"type":        "create_task",
				"description": "去创建",
			},
		},
	})
	if !bytes.Contains(callQ, []byte("emitted")) || !bytes.Contains(callQ, []byte("create_task")) {
		t.Fatalf("qualified tools/call=%s", callQ)
	}
}

func TestServer_ToolsCallClientFlow_HubFailureReturnsToolError(t *testing.T) {
	hub := core.NewAskUserHub()
	srv, err := Start(hub)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close(context.Background())

	call := postRPC(t, srv.MCPURL(), "sess-without-emitter", map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": core.ToolCCConnectClientFlow,
			"arguments": map[string]any{
				"type":        "connect_account",
				"description": "绑定新账户",
			},
		},
	})
	if !bytes.Contains(call, []byte(`"isError":true`)) {
		t.Fatalf("expected MCP tool error result: %s", call)
	}
	if !bytes.Contains(call, []byte("client_flow failed:")) {
		t.Fatalf("expected client_flow failure text: %s", call)
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

func postRPCUnix(t *testing.T, socketPath, sessionKey string, body any) []byte {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}
	return postRPCWithClient(t, client, "http://localhost/mcp", sessionKey, body)
}

func postRPC(t *testing.T, url, sessionKey string, body any) []byte {
	t.Helper()
	return postRPCWithClient(t, http.DefaultClient, url, sessionKey, body)
}

func postRPCWithClient(t *testing.T, client *http.Client, url, sessionKey string, body any) []byte {
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
	resp, err := client.Do(req)
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
