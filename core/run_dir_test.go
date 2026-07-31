package core

import (
	"path/filepath"
	"testing"
)

func TestResolveRunDir(t *testing.T) {
	t.Parallel()
	dataDir := filepath.Join(t.TempDir(), "state")
	defaultWant := filepath.Join(dataDir, "run")

	if got := ResolveRunDir(dataDir, ""); got != defaultWant {
		t.Fatalf("default = %q, want %q", got, defaultWant)
	}
	if got := ResolveRunDir(dataDir, "/tmp/pod-run"); got != "/tmp/pod-run" {
		t.Fatalf("configured = %q, want /tmp/pod-run", got)
	}
	if got := ResolveRunDir(dataDir, "  /tmp/pod-run-trimmed  "); got != "/tmp/pod-run-trimmed" {
		t.Fatalf("trimmed configured = %q, want /tmp/pod-run-trimmed", got)
	}
}
