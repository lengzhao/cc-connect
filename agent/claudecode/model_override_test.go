package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestStartSessionWithModelPreservesProviderAndDefault(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "args")
	script := filepath.Join(dir, "claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+capture+"\"\ncat >/dev/null\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cmd: script, workDir: dir, model: "default", providers: []core.ProviderConfig{{Name: "provider", Model: "provider-default"}}, activeIdx: 0}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := a.StartSessionWithModel(ctx, "original-history", "routed-model")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var args string
	for i := 0; i < 100; i++ {
		b, _ := os.ReadFile(capture)
		args = string(b)
		if strings.Contains(args, "routed-model") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(args, "--model\nrouted-model") || !strings.Contains(args, "--resume\noriginal-history") {
		t.Fatalf("wrong CLI args: %s", args)
	}
	if a.model != "default" || a.providers[0].Model != "provider-default" {
		t.Fatal("mutated shared model")
	}
}
