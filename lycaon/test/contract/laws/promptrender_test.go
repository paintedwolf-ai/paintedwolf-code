package laws_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/protectedpath"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"

	"github.com/lycaon/lycaon/test/contract/laws"
	"github.com/lycaon/lycaon/test/contract/laws/promptrender"
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
	stale.Exempt = []laws.Exemption{{Key: "config/packs/painted-wolf/platform/guidance/retired.md: r.path", Reason: "fixture"}}
	for name, tc := range map[string]struct {
		rule       laws.Rule
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
			expect(t, laws.Evaluate(fixture(t, tc.files), tc.rule), tc.want, tc.violations)
		})
	}
}

const seededKick = "coordinator-roots-changed"

// P13 on the coordinator kick row: a guidance template, served through the
// checkout's prompt overlay, that names a tool no coordinator surface offers
// fails on every surface; the stock template and a rewording of it that keeps
// its names pass.
func TestPromptRenderReachFailsOnASeededTemplate(t *testing.T) {
	t.Parallel()
	kicks := rule(t, promptrender.Family, "coordinator-kicks-name-only-offered-tools")
	stockKick, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), guidance+seededKick+".md"))
	contractcheck.FailErr(t, "read the stock kick", err)
	overlay := "lycaon/" + protectedpath.PromptFilesDirName + "/kicks/" + seededKick + ".md"
	anchors := map[string]string{
		"lycaon/internal/prompts/render.go": "package prompts\n\ntype FileTemplateEngine struct{}\n\nfunc (e *FileTemplateEngine) RenderKick() {}\n",
	}
	reworded := "To keep you oriented: " + strings.ReplaceAll(string(stockKick), "\n\n", "\n\nIn addition: ")
	seeded := string(stockKick) + "\nTo settle this, call `complete_leg`.\n"
	// No tool schema has wait_ms, so the argument is stale wherever it is
	// attributed.
	staleArg := string(stockKick) + "\nAsk again with `ask_user(prompt=…, wait_ms=500)`.\n"
	ownArg := string(stockKick) + "{% if profile_has_ask_user %}\nAsk again with `ask_user(prompt=…)`.\n{% endif %}"
	for name, tc := range map[string]struct {
		overlay string
		want    outcome
		names   []string
	}{
		"stock template":  {want: passes},
		"reworded":        {overlay: reworded, want: passes},
		"seeded tool":     {overlay: seeded, want: violates, names: []string{"complete_leg"}},
		"seeded argument": {overlay: staleArg, want: violates, names: []string{"wait_ms", "ask_user"}},
		"own argument":    {overlay: ownArg, want: passes},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := with(anchors, map[string]string{overlay: tc.overlay})
			res := laws.Evaluate(fixture(t, files), kicks)
			expect(t, res, tc.want, 0)
			for _, v := range res.Violations {
				if !strings.Contains(v, "kick "+seededKick+" ") || !slices.ContainsFunc(tc.names, func(n string) bool { return strings.Contains(v, n) }) {
					t.Errorf("violation outside the seeded template and %v: %s", tc.names, v)
				}
			}
			if len(tc.names) > 0 && !slices.ContainsFunc(res.Violations, func(v string) bool { return strings.Contains(v, tc.names[0]) }) {
				t.Errorf("no violation reports %s", tc.names[0])
			}
		})
	}
}
