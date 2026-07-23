package claudecode

import (
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
	if shouldUseMCPAsk("native", "http://x/mcp", "sk", hub) {
		t.Fatal("native must not use MCP")
	}
	if !shouldUseMCPAsk("mcp", "http://x/mcp", "sk", hub) {
		t.Fatal("mcp ready should use MCP")
	}
	if shouldUseMCPAsk("mcp", "", "sk", hub) {
		t.Fatal("mcp without URL must not use MCP")
	}
	if !shouldUseMCPAsk("hybrid", "http://x/mcp", "sk", hub) {
		t.Fatal("hybrid ready should use MCP")
	}
	if shouldUseMCPAsk("hybrid", "http://x/mcp", "", hub) {
		t.Fatal("hybrid without session key falls back to native")
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
