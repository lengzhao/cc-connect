package claudecode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContextResourcesIncludesAutomonSkillsMemoryAndKnowledge(t *testing.T) {
	workDir := t.TempDir()
	configDir := filepath.Join(t.TempDir(), ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	automonPath := filepath.Join(configDir, "AUTOMON.md")
	files := map[string]string{
		automonPath: "# Automon\n",
		filepath.Join(configDir, "skills", "daily", "SKILL.md"):        "# Daily\n",
		filepath.Join(workDir, "files", "memory", "correction.md"):     "# Correction\n",
		filepath.Join(workDir, "files", "knowledge", "company-sop.md"): "# SOP\n",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	agent := &Agent{workDir: workDir, appendSystemPromptFiles: []string{automonPath}}
	resources, err := agent.ContextResources()
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	versions := map[string]string{}
	for _, resource := range resources {
		kinds[resource.Kind] = true
		versions[resource.Kind+":"+resource.Path] = resource.Version
	}
	for _, kind := range []string{"automon", "skill", "memory", "knowledge"} {
		if !kinds[kind] {
			t.Fatalf("missing %s resource: %+v", kind, resources)
		}
	}

	before := versions["memory:files/memory/correction.md"]
	if err := os.WriteFile(filepath.Join(workDir, "files", "memory", "correction.md"), []byte("# Updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resources, err = agent.ContextResources()
	if err != nil {
		t.Fatal(err)
	}
	var after string
	for _, resource := range resources {
		if resource.Kind == "memory" && resource.Path == "files/memory/correction.md" {
			after = resource.Version
		}
	}
	if before == "" || after == "" || before == after {
		t.Fatalf("memory version before=%q after=%q", before, after)
	}
}
