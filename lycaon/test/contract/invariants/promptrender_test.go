package invariants_test

import (
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

const guidance = "lycaon/config/packs/painted-wolf/platform/guidance/"

var templateTree = map[string]string{
	guidance + "coordinator-roots-changed.md":                                      "Roots changed: {{ root.label }}.\n",
	guidance + "editor/inline-edit.md":                                             "Editing {{ path }}.\n",
	guidance + "curator/user.md":                                                   "Evidence at {{ c.path }}.\n",
	"lycaon/config/packs/painted-wolf/platform/shared/partials/command-surface.md": "{% if session_scratch_dir %}scratch{% endif %}\n",
}

// The family fails on each kind of drift, shown on the template host-path
// row; the rendering rows read the stock catalog.
func TestPromptRenderFailsClosed(t *testing.T) {
	t.Parallel()
	hostPaths := rule(t, promptrender.Family, "templates-render-no-host-paths")
	stale := bare(hostPaths)
	stale.Exempt = []invariants.Exemption{{Key: "config/packs/painted-wolf/platform/guidance/retired.md: r.path", Reason: "fixture"}}
	for name, tc := range map[string]struct {
		rule       invariants.Invariant
		files      map[string]string
		want       outcome
		violations int
	}{
		"clean": {rule: hostPaths, files: templateTree, want: passes},
		// Every host path variable is caught, and a coordinator kick may not
		// render a bare path.
		"seeded violation": {rule: hostPaths, files: with(templateTree, map[string]string{
			guidance + "coordinator-roots-changed.md": "{{ r.path }} {{ c.old_path }} {{ new_primary.path }} {{ active_root.path }} {{ root.path }} {{ project_dir }} {{ session_scratch_dir }} {{ worker_workspace_path }} {{ path }}\n",
		}), want: violates, violations: 9},
		"renamed anchor": {rule: hostPaths, files: map[string]string{
			"lycaon/config/packs/painted-wolf/platform/notes/x.md": "{{ path }}\n",
		}, want: missesAnchor},
		"stale exemption": {rule: stale, files: templateTree, want: staleExemption},
		"empty corpus": {rule: hostPaths, files: map[string]string{
			guidance + "notes.txt": "not a template\n",
		}, want: fails},
		"unparsable file": {rule: rule(t, promptrender.Family, "skills-fit-their-budgets"), files: map[string]string{
			"lycaon/internal/skills/budget.go": "package skills\n\nconst BundledBodyBudget = (\n",
		}, want: fails},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			expect(t, invariants.Evaluate(fixture(t, tc.files), tc.rule), tc.want, tc.violations)
		})
	}
}
