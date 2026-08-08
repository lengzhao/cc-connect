package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsRegisteredSlashCommand_Builtin(t *testing.T) {
	e := newTestEngine()
	if !e.isRegisteredSlashCommand("/list") {
		t.Fatal("expected /list to be a registered slash command")
	}
	if e.isRegisteredSlashCommand("hello") {
		t.Fatal("expected plain text to be unregistered")
	}
}

func TestIsRegisteredSlashCommand_Custom(t *testing.T) {
	e := newTestEngine()
	e.AddCommand("skills-sync", "sync skills", "", "echo ok", "", "config")
	if !e.isRegisteredSlashCommand("/skills-sync") {
		t.Fatal("expected custom command /skills-sync to be registered")
	}
	if !e.isRegisteredSlashCommand("/skills-sync -ws") {
		t.Fatal("expected custom command with args to be registered")
	}
}

func TestIsRegisteredSlashCommand_Skill(t *testing.T) {
	e := newTestEngine()
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "deploy-prod")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("deploy"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.skills.SetDirs([]string{dir})

	if !e.isRegisteredSlashCommand("/deploy-prod") {
		t.Fatal("expected skill command /deploy-prod to be registered")
	}
}

func TestWorkspaceInitFlow_CustomCommandPassesThroughWithLocalPaths(t *testing.T) {
	baseDir := t.TempDir()
	e := newTestEngineWithMultiWorkspace(t, baseDir)
	e.SetWorkspaceInitAllowLocalPaths(true)
	e.AddCommand("skills-sync", "sync skills", "", "echo ok", "", "config")

	p := &mockChannelResolver{names: map[string]string{"C010": "test-channel"}}
	msg := &Message{SessionKey: "mock:C010:user1", Content: "/skills-sync"}

	if consumed := e.handleWorkspaceInitFlow(p, msg, "test-channel"); consumed {
		t.Fatal("expected custom slash command to bypass workspace init flow")
	}
}

func TestWorkspaceInitFlow_CustomCommandCleansUpExistingFlow(t *testing.T) {
	baseDir := t.TempDir()
	e := newTestEngineWithMultiWorkspace(t, baseDir)
	e.SetWorkspaceInitAllowLocalPaths(true)
	e.AddCommand("agenthub-sync", "sync agenthub", "", "echo ok", "", "config")

	p := &mockChannelResolver{names: map[string]string{"C010": "test-channel"}}
	channelKey := workspaceChannelKey(p.Name(), "C010")

	e.initFlowsMu.Lock()
	e.initFlows[channelKey] = &workspaceInitFlow{
		state:       "awaiting_url",
		channelName: "test-channel",
	}
	e.initFlowsMu.Unlock()

	msg := &Message{SessionKey: "mock:C010:user1", Content: "/agenthub-sync"}
	if consumed := e.handleWorkspaceInitFlow(p, msg, "test-channel"); consumed {
		t.Fatal("expected custom slash command to bypass existing init flow")
	}

	e.initFlowsMu.Lock()
	_, stillExists := e.initFlows[channelKey]
	e.initFlowsMu.Unlock()
	if stillExists {
		t.Fatal("expected init flow to be deleted for registered slash command")
	}
}
