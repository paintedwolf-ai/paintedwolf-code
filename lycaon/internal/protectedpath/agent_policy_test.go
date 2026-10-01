package protectedpath_test

import (
	"regexp"
	"testing"

	"github.com/lycaon/lycaon/internal/protectedpath"
)

func TestAgentPolicyLocationsCoverEveryLoader(t *testing.T) {
	cases := []struct {
		rel     string
		surface string
	}{
		{"AGENTS.md", protectedpath.SurfaceAgentsMD},
		{"pkg/deep/AGENTS.md", protectedpath.SurfaceAgentsMD},
		{"agents.md", protectedpath.SurfaceAgentsMD},
		{"AGENTS.md::$DATA", protectedpath.SurfaceAgentsMD},
		{"AGENTS.md.", protectedpath.SurfaceAgentsMD},
		{".paintedwolf/AGENTS.md", protectedpath.SurfaceAgentsMD},
		{".paintedwolf/skills/review/SKILL.md", protectedpath.SurfaceSkills},
		{".agents/skills/review/references/checklist.md", protectedpath.SurfaceSkills},
		{".paintedwolf/prompt_files/partials/agent-skills.md", protectedpath.SurfacePromptOverrides},
		{".paintedwolf/approvals.yaml", protectedpath.SurfaceProjectSettings},
		{".paintedwolf/postures.yaml", protectedpath.SurfaceProjectSettings},
		{".paintedwolf/overlay.yaml", protectedpath.SurfaceProjectSettings},
		{".paintedwolf/rules/deny-network.yaml", protectedpath.SurfaceProjectSettings},
		{".paintedwolf/workflows/review/workflow.yaml", protectedpath.SurfaceProjectSettings},
		{".paintedwolf/mcp.yaml", protectedpath.SurfaceProjectMCP},
		{".paintedwolf/extensions.lock.yaml", protectedpath.SurfaceExtensionConfig},
		{".paintedwolf/detection-packs.yaml", protectedpath.SurfaceScanConfig},
		// Filesystem respellings open the same file.
		{".paintedwolf/approvals.yaml::$DATA", protectedpath.SurfaceProjectSettings},
		{".paintedwolf/mcp.yaml.", protectedpath.SurfaceProjectMCP},
		{".paintedwolf./approvals.yaml ", protectedpath.SurfaceProjectSettings},
		{".PAINTEDWOLF/Approvals.yaml", protectedpath.SurfaceProjectSettings},
		{".paintedwolf::$DATA/skills/review/SKILL.md", protectedpath.SurfaceSkills},
	}
	for _, tc := range cases {
		if got := surfaceFor(tc.rel); got != tc.surface {
			t.Errorf("%q surface = %q, want %q", tc.rel, got, tc.surface)
		}
	}
	for _, rel := range []string{
		"README.md", "pkg/SKILL.md", "skills/review/SKILL.md", "../AGENTS.md", "../.paintedwolf/approvals.yaml", ".", "",
		".paintedwolf/blueprints/plan.md", ".paintedwolf/scratch/notes.txt", "pkg/.paintedwolf/approvals.yaml",
		"lycaon/config/packs/painted-wolf/security/host/approvals.yaml",
	} {
		if got := surfaceFor(rel); got != "" {
			t.Errorf("%q is agent policy (%s); it is read by no loader", rel, got)
		}
	}
}

func surfaceFor(rel string) string {
	for _, location := range protectedpath.AgentPolicyLocations() {
		if location.Contains(rel) {
			return location.Surface
		}
	}
	return ""
}

// The kernel reads the regex and the host reads Contains; they must agree.
func TestAgentPolicyNameRegexAgreesWithContains(t *testing.T) {
	const root = "/work/project"
	for _, location := range protectedpath.AgentPolicyLocations() {
		if location.Name == "" {
			continue
		}
		re := regexp.MustCompile(location.NameRegex())
		for _, rel := range []string{"AGENTS.md", "pkg/agents.MD", "a/b/Agents.Md", "README.md", "AGENTS.md.bak", "xAGENTS.md", "pkg/AGENTS.mdx"} {
			if kernel, host := re.MatchString(root+"/"+rel), location.Contains(rel); kernel != host {
				t.Errorf("%s: regex=%v contains=%v for %q", location.Surface, kernel, host, rel)
			}
		}
	}
}
