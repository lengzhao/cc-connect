package claudecode

import (
	"bytes"
	"os"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNormalizeAskUserMode(t *testing.T) {
	cases := map[string]string{
		"":       "mcp",
		"mcp":    "mcp",
		"MCP":    "mcp",
		"native": "native",
		"hybrid": "hybrid",
		"other":  "mcp",
	}
	for in, want := range cases {
		if got := normalizeAskUserMode(in); got != want {
			t.Fatalf("normalizeAskUserMode(%q)=%q want %q", in, got, want)
		}
	}
}

func TestShouldUseMCPAsk(t *testing.T) {
	hub := core.NewAskUserHub()
	if shouldUseMCPAsk("native", "http://x/mcp", "/tmp/ask.sock", "sk", hub) {
		t.Fatal("native must not use MCP")
	}
	if !shouldUseMCPAsk("mcp", "http://x/mcp", "/tmp/ask.sock", "sk", hub) {
		t.Fatal("mcp ready should use MCP")
	}
	if !shouldUseMCPAsk("mcp", "", "/tmp/ask.sock", "sk", hub) {
		t.Fatal("mcp with unix socket should use MCP")
	}
	if shouldUseMCPAsk("mcp", "http://x/mcp", "", "sk", hub) {
		t.Fatal("stdio mcp without socket must not use MCP")
	}
	if !shouldUseMCPAsk("hybrid", "http://x/mcp", "/tmp/ask.sock", "sk", hub) {
		t.Fatal("hybrid ready should use MCP")
	}
	if shouldUseMCPAsk("hybrid", "http://x/mcp", "/tmp/ask.sock", "", hub) {
		t.Fatal("hybrid without session key falls back to native")
	}
}

func TestWriteAskUserMCPConfig_Stdio(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/mcp.json"
	if err := writeAskUserMCPConfig(path, "/tmp/x.sock", "sk"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !bytes.Contains(b, []byte(`"type": "stdio"`)) || !bytes.Contains(b, []byte(`"askuser-mcp-stdio"`)) {
		t.Fatalf("stdio config=%s", b)
	}
	if bytes.Contains(b, []byte("socketPath")) || bytes.Contains(b, []byte(`"url"`)) {
		t.Fatalf("stdio config must not use http/socketPath: %s", b)
	}
}

func TestAgent_AskUserSupportSnapshot(t *testing.T) {
	hub := core.NewAskUserHub()
	a := &Agent{}
	a.SetAskUserSupport("  http://127.0.0.1:12345/mcp  ", "/tmp/ask.sock", hub)

	gotURL, gotSocket, gotHub := a.AskUserSupport()
	if gotURL != "http://127.0.0.1:12345/mcp" {
		t.Fatalf("AskUserSupport URL = %q", gotURL)
	}
	if gotSocket != "/tmp/ask.sock" {
		t.Fatalf("AskUserSupport socket = %q", gotSocket)
	}
	if gotHub != hub {
		t.Fatalf("AskUserSupport hub = %p, want %p", gotHub, hub)
	}
}

func TestEnsureToolListed(t *testing.T) {
	got := ensureToolListed(nil, "AskUserQuestion")
	if len(got) != 1 || got[0] != "AskUserQuestion" {
		t.Fatalf("%v", got)
	}
	got = ensureToolListed([]string{"Read", "AskUserQuestion"}, "AskUserQuestion")
	if len(got) != 2 {
		t.Fatalf("duplicate insert: %v", got)
	}
}
