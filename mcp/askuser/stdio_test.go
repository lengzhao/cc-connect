package askuser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestWriteMCPConfig_StdioBridge(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/mcp.json"
	sock := dir + "/askuser-mcp.sock"

	if err := WriteMCPConfig(path, "/usr/local/bin/cc-connect", sock, "proj:chat:u1"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"type": "stdio"`)) {
		t.Fatalf("config must use stdio MCP: %s", b)
	}
	if !bytes.Contains(b, []byte(`"command": "/usr/local/bin/cc-connect"`)) {
		t.Fatalf("missing command: %s", b)
	}
	for _, want := range []string{`"askuser-mcp-stdio"`, `"CC_CONNECT_ASKUSER_SOCKET"`, sock, `"CC_CONNECT_SESSION_KEY"`, `"proj:chat:u1"`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("missing %q in config: %s", want, b)
		}
	}
	if bytes.Contains(b, []byte("--session-key")) || bytes.Contains(b, []byte("socketPath")) || bytes.Contains(b, []byte(`"url"`)) {
		t.Fatalf("stdio config must not expose key in args or depend on http/socketPath: %s", b)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode=%#o want 0600", got)
	}
}

func TestServeStdio_ProxiesToolsListOverUnixSocket(t *testing.T) {
	hub := core.NewAskUserHub()
	srv, err := StartUnix(hub, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close(context.Background())

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}
`)
	var out bytes.Buffer
	if err := ServeStdio(context.Background(), in, &out, srv.SocketPath(), "sess-1"); err != nil {
		t.Fatal(err)
	}

	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("stdio response is not JSON: %q err=%v", out.String(), err)
	}
	if resp.ID != 1 {
		t.Fatalf("id=%d", resp.ID)
	}
	names := map[string]bool{}
	for _, tool := range resp.Result.Tools {
		names[tool.Name] = true
	}
	if !names[core.ToolCCConnectAskUser] || !names[core.ToolCCConnectClientFlow] {
		t.Fatalf("tools=%v response=%s", names, out.String())
	}
}

func TestServeStdio_ProxiesContentLengthFramedMessage(t *testing.T) {
	hub := core.NewAskUserHub()
	srv, err := StartUnix(hub, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close(context.Background())

	msg := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	in := strings.NewReader(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(msg), msg))
	var out bytes.Buffer
	if err := ServeStdio(context.Background(), in, &out, srv.SocketPath(), "sess-1"); err != nil {
		t.Fatal(err)
	}
	raw := out.String()
	if !strings.HasPrefix(raw, "Content-Length: ") {
		t.Fatalf("expected header-framed response, got %q", raw)
	}
	_, body, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		t.Fatalf("missing header/body split: %q", raw)
	}
	if !bytes.Contains([]byte(body), []byte(core.ToolCCConnectAskUser)) {
		t.Fatalf("response body=%s", body)
	}
}

func TestServeStdio_ProxiesAskUserToolCall(t *testing.T) {
	hub := core.NewAskUserHub()
	srv, err := StartUnix(hub, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close(context.Background())

	hub.Bind("sess-ask", askEmitterFunc(func(e core.Event) error {
		go func() {
			time.Sleep(10 * time.Millisecond)
			hub.Complete(e.RequestID, map[int]string{0: "banana"}, map[int]string{0: "香蕉"})
		}()
		return nil
	}))

	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"cc_connect_ask_user","arguments":{"question":"选一个水果","options":[{"label":"苹果","value":"apple"},{"label":"香蕉","value":"banana"},{"label":"橙子","value":"orange"}]}}}
`)
	var out bytes.Buffer
	if err := ServeStdio(context.Background(), in, &out, srv.SocketPath(), "sess-ask"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("banana")) || !bytes.Contains(out.Bytes(), []byte("香蕉")) {
		t.Fatalf("tools/call response=%s", out.String())
	}
}
