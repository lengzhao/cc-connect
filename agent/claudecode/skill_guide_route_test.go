package claudecode

import (
	"strings"
	"testing"
)

func TestPrependSkillGuideTrainingRoute(t *testing.T) {
	bootstrap := `[Authoritative Automon training context]
tenantId: nex-workbench:xiang.gu`

	t.Run("prepends for training @skill-guide", func(t *testing.T) {
		in := "@skill-guide 提交审批\n\n" + bootstrap
		got := prependSkillGuideTrainingRoute(in)
		if got == in {
			t.Fatal("expected training route prefix")
		}
		for _, want := range []string{"@skill-guide 提交审批", bootstrap, "subagent_type=skill-guide"} {
			if !strings.Contains(got, want) {
				t.Fatalf("missing %q in %q", want, got)
			}
		}
	})

	t.Run("prepends for follow-up training routing block", func(t *testing.T) {
		in := "@skill-guide inspect skill\n\n[Training routing]\n- delegate"
		got := prependSkillGuideTrainingRoute(in)
		if got == in {
			t.Fatal("expected training route prefix for follow-up routing block")
		}
	})

	t.Run("no-op without training bootstrap", func(t *testing.T) {
		in := "@skill-guide hello"
		if got := prependSkillGuideTrainingRoute(in); got != in {
			t.Fatalf("got %q, want unchanged", got)
		}
	})

	t.Run("no-op without @skill-guide prefix", func(t *testing.T) {
		in := "save draft\n\n" + bootstrap
		if got := prependSkillGuideTrainingRoute(in); got != in {
			t.Fatalf("got %q, want unchanged", got)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		in := "@skill-guide save\n\n" + bootstrap
		once := prependSkillGuideTrainingRoute(in)
		twice := prependSkillGuideTrainingRoute(once)
		if once != twice {
			t.Fatalf("expected idempotent prepend")
		}
	})
}
