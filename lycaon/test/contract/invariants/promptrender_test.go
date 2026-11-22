package invariants_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/protectedpath"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"

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
			res := invariants.Evaluate(fixture(t, files), kicks)
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

func TestCapabilityConditionsAndSourceDiscoveryFailClosed(t *testing.T) {
	t.Parallel()
	capRule := rule(t, promptrender.Family, "capability-conditions-use-declared-facts")
	discRule := rule(t, promptrender.Family, "stock-sources-reconciled-and-parsed")

	anchors := map[string]string{
		"lycaon/internal/prompts/reader_facts_vocab.go": "package prompts\n\nfunc CheckTemplateVocabulary() {}\n",
		"lycaon/internal/prompts/persona_render.go":     "package prompts\n\nfunc RenderPersona() {}\n",
	}

	const (
		skillDir     = "lycaon/config/packs/painted-wolf/platform/skills/seeded-release/"
		skillBodyRel = skillDir + "SKILL.md"
		skillRefRel  = skillDir + "references/seeded-manifest.md"
		promptRel    = "lycaon/config/packs/painted-wolf/platform/agents/prompts/seeded-agent.md"
		partialRel   = "lycaon/config/packs/painted-wolf/platform/shared/partials/seeded-host.md"
	)

	t.Run("passing controls for valid capability guards and presentation logic", func(t *testing.T) {
		t.Parallel()
		files := with(anchors, map[string]string{
			skillBodyRel: "---\nname: seeded-release\ndescription: Valid control skill.\nmetadata:\n  paintedwolf.any_of_tools: command, verify\n---\n" +
				"{% if multi_root %}Workspace spans {{ root_count }} folders.{% endif %}\n" +
				"{% if has_git and not on_worker_branch %}Resolve commit, tag, and version.{% else %}Take version from the brief.{% endif %}\n" +
				"{% if can_git_read %}Inspect `git log` in `command`.{% elif can_request_git_read %}Call `request_tools(need=\"command\")`.{% endif %}\n" +
				"{% if profile_has_verify %}Run `verify`.{% endif %}\n" +
				"{% if can_command_external_selector %}Run `external -- lsof +L1`.{% endif %}\n",
			partialRel: "{% if runner_host_resources_tools %}Declare id in `capability_request.host_resources`.{% endif %}\n",
			promptRel:  "{% set finish_note = \"Report grounded results.\" %}{{ finish_note }}\n{% include \"partials/seeded-host.md\" %}\n",
		})
		expect(t, invariants.Evaluate(fixture(t, files), discRule), passes, 0)
		expect(t, invariants.Evaluate(fixture(t, files), capRule), passes, 0)
	})

	for name, tc := range map[string]struct {
		rule    invariants.Invariant
		files   map[string]string
		expects []string
	}{
		"seeded local set alias over real vocabulary promising unavailable git action": {
			rule: capRule,
			files: map[string]string{
				skillBodyRel: "---\nname: seeded-release\ndescription: Seeded alias.\n---\n" +
					"{% set git_ok = has_git and not on_worker_branch %}\n" +
					"{% if git_ok %}Run `git log previous..release` in `command`.{% endif %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/skills/seeded-release/SKILL.md",
				"has_git and not on_worker_branch",
				"git_ok",
			},
		},
		"seeded inline workspace condition promising unestablished command and verify": {
			rule: capRule,
			files: map[string]string{
				skillBodyRel: "---\nname: seeded-release\ndescription: Seeded inline.\n---\n" +
					"{% if has_git and not on_worker_branch %}Confirm `git status --short` in `command` and run `verify`.{% endif %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/skills/seeded-release/SKILL.md",
				"has_git and not on_worker_branch",
				"verify",
			},
		},
		"seeded violation inside included partial": {
			rule: capRule,
			files: map[string]string{
				partialRel: "{% if (profile_has_command and profile_command_external_exec != false) or profile_has_verify %}Include id in `capability_request.host_resources`.{% endif %}\n",
				promptRel:  "## Host\n{% include \"partials/seeded-host.md\" %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/shared/partials/seeded-host.md",
				"profile_has_verify",
				"capability_request.host_resources",
			},
		},
		"seeded violation inside skill reference file": {
			rule: capRule,
			files: map[string]string{
				skillBodyRel: "---\nname: seeded-release\ndescription: Valid body.\nmetadata:\n  paintedwolf.any_of_tools: command, verify\n---\n{% if profile_has_verify %}Run `verify`.{% endif %}\n",
				skillRefRel:  "{% if not on_worker_branch %}Compute `git log` in `command` and `external -- lsof +L1`.{% endif %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/skills/seeded-release/references/seeded-manifest.md",
				"not on_worker_branch",
				"can_command_external_selector",
			},
		},
		"seeded static tool_profile check reconstructing capability": {
			rule: capRule,
			files: map[string]string{
				partialRel: "{% if tool_profile == \"implement\" %}Request access in `capability_request`.{% endif %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/shared/partials/seeded-host.md",
				`tool_profile == \"implement\"`,
				"checks static tool_profile identity",
			},
		},
		"newly added unclassified stock markdown source fails discovery": {
			rule: discRule,
			files: map[string]string{
				"lycaon/config/packs/painted-wolf/platform/unreviewed/rogue-prompt.md": "Call `verify`.\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/unreviewed/rogue-prompt.md",
				"unclassified",
			},
		},
		"unhandled executable Pongo tag fails closed naming source": {
			rule: discRule,
			files: map[string]string{
				partialRel: "{% custom_exec profile_has_command %}run{% endcustom_exec %}\n",
			},
			expects: []string{
				"config/packs/painted-wolf/platform/shared/partials/seeded-host.md",
				"custom_exec",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := invariants.Evaluate(fixture(t, with(anchors, tc.files)), tc.rule)
			expect(t, res, violates, 0)
			joined := strings.Join(res.Violations, "\n")
			for _, want := range tc.expects {
				if !strings.Contains(joined, want) {
					t.Errorf("violations missing %q:\n%s", want, joined)
				}
			}
		})
	}
}
