package inject_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenderSkillProcedureBlockGatesResourceInstructionOnResources(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)

	resourceless, err := inject.RenderSkillProcedureBlock(t.Context(), renderer, "s1", "coordinator", turnload.SkillPreload{
		Name: "prepare-a-release",
		Body: "Check the version file.",
	})
	testutil.FailErr(t, "render resourceless skill procedure", err)
	if !strings.Contains(resourceless, "### Procedure: prepare-a-release") || !strings.Contains(resourceless, "Check the version file.") {
		t.Fatalf("missing procedure header or body:\n%s", resourceless)
	}
	if strings.Contains(resourceless, "skills_read") {
		t.Fatalf("resourceless skill procedure advertised skills_read:\n%s", resourceless)
	}

	withResources, err := inject.RenderSkillProcedureBlock(t.Context(), renderer, "s1", "coordinator", turnload.SkillPreload{
		Name:      "verify-a-change",
		Body:      "Run the checks.",
		Tool:      "verify",
		Resources: []string{"references/failure-triage.md", "scripts/check.sh"},
	})
	testutil.FailErr(t, "render resource-bearing skill procedure", err)
	for _, want := range []string{
		"when `verify` was first called",
		`skills_read({"need":"verify-a-change","resource":"relative/path"})`,
		"references/failure-triage.md, scripts/check.sh",
	} {
		if !strings.Contains(withResources, want) {
			t.Fatalf("resource-bearing procedure missing %q:\n%s", want, withResources)
		}
	}
}
