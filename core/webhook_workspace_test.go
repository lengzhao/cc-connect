package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCUJ_WebhookWorkspaceQueue(t *testing.T) {
	p := &cujReplyCtxPlatform{stubPlatformEngine: &stubPlatformEngine{n: "lark"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	base := t.TempDir()
	e.SetMultiWorkspace(base, filepath.Join(t.TempDir(), "bindings.json"))
	dir := filepath.Join(base, "team")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	dir = normalizeWorkspacePath(dir)
	e.workspaceBindings.Bind("project:test", "oc_chat", "team", dir)
	as := newCUJAgentSession()
	as.delayMs = 100
	a := &sessionEnvRecordingAgent{session: as}
	w := e.workspacePool.GetOrCreate(dir)
	w.agent = a
	w.sessions = NewSessionManager("")
	key := "lark:oc_chat:root:om_root"
	session := w.sessions.GetOrCreateActive(key)
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("timed out")
	}
	send := func(content string) {
		e.ReceiveMessage(p, &Message{SessionKey: key, Platform: "lark", UserID: "alice", UserName: "Alice", Content: content, ReplyCtx: "ctx"})
	}
	send("first context")
	wait(func() bool { return len(as.getSentPrompts()) == 1 })
	(&WebhookServer{}).executePrompt(e, key, "/model keep this as plain prompt", true, "followup")
	wait(func() bool { return len(as.getSentPrompts()) == 2 && !session.Busy() })
	send("third followup")
	wait(func() bool { return len(as.getSentPrompts()) == 3 && !session.Busy() })
	prompts := as.getSentPrompts()
	for i, want := range []string{"first context", "/model keep this as plain prompt", "third followup"} {
		if !strings.Contains(prompts[i], want) {
			t.Fatalf("prompt %d: %q", i, prompts[i])
		}
	}
	if w.sessions.GetOrCreateActive(key) != session {
		t.Fatal("session changed")
	}
	if got := a.EnvValue("CC_HOOK_PROJECT"); got != "test" {
		t.Fatalf("project=%q", got)
	}
	if got := a.EnvValue("CC_SESSION_KEY"); got != key {
		t.Fatalf("key=%q", got)
	}
	count := 0
	for _, s := range p.getSent() {
		if strings.Contains(s, "ok") {
			count++
		}
	}
	if count < 3 {
		t.Fatalf("expected three visible replies: %v", p.getSent())
	}
}

func TestWebhookWithoutWorkspaceDoesNotUseGlobalAgent(t *testing.T) {
	p := &cujReplyCtxPlatform{stubPlatformEngine: &stubPlatformEngine{n: "lark"}}
	a := &cujAgent{}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))
	(&WebhookServer{}).executePrompt(e, "lark:oc_missing:root:om_root", "/model plain prompt", true, "followup")
	time.Sleep(50 * time.Millisecond)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sessions) != 0 {
		t.Fatal("injection fell back to global agent")
	}
}
