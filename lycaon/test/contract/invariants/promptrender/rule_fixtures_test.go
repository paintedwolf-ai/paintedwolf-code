package promptrender_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"

	"github.com/lycaon/lycaon/test/contract/invariants/invariantstest"

	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

const guidanceDir = "lycaon/config/packs/painted-wolf/platform/guidance/"

var templateTree = map[string]string{
	guidanceDir + "coordinator-roots-changed.md":                                   "Roots changed: {{ root.label }}.\n",
	guidanceDir + "editor/inline-edit.md":                                          "Editing {{ path }}.\n",
	guidanceDir + "curator/user.md":                                                "Evidence at {{ c.path }}.\n",
	"lycaon/config/packs/painted-wolf/platform/shared/partials/command-surface.md": "{% if session_scratch_dir %}scratch{% endif %}\n",
}

// The family invariantstest.Fails on each kind of drift, shown on the template host-path
// row; the rendering rows read the stock catalog.
func TestPromptRenderFailsClosed(t *testing.T) {
	t.Parallel()
	hostPaths := invariantstest.Rule(t, promptrender.Family, "templates-render-no-host-paths")
	stale := invariantstest.Bare(hostPaths)
	stale.Exempt = []invariants.Exemption{{Key: "config/packs/painted-wolf/platform/guidance/retired.md: r.path", Reason: "fixture"}}
	for name, tc := range map[string]struct {
		rule       invariants.Invariant
		files      map[string]string
		want       invariantstest.Outcome
		violations int
	}{
		"clean": {rule: hostPaths, files: templateTree, want: invariantstest.Passes},
		// Every host path variable is caught, and a coordinator kick may not
		// render a bare path.
		"seeded violation": {rule: hostPaths, files: invariantstest.With(templateTree, map[string]string{
			guidanceDir + "coordinator-roots-changed.md": "{{ r.path }} {{ c.old_path }} {{ new_primary.path }} {{ active_root.path }} {{ root.path }} {{ project_dir }} {{ session_scratch_dir }} {{ worker_workspace_path }} {{ path }}\n",
		}), want: invariantstest.Violates, violations: 9},
		"renamed anchor": {rule: hostPaths, files: map[string]string{
			"lycaon/config/packs/painted-wolf/platform/notes/x.md": "{{ path }}\n",
		}, want: invariantstest.MissesAnchor},
		"stale exemption": {rule: stale, files: templateTree, want: invariantstest.StaleExemption},
		"empty corpus": {rule: hostPaths, files: map[string]string{
			guidanceDir + "notes.txt": "not a template\n",
		}, want: invariantstest.Fails},
		"unparsable file": {rule: invariantstest.Rule(t, promptrender.Family, "skills-fit-their-budgets"), files: map[string]string{
			"lycaon/internal/skills/budget.go": "package skills\n\nconst BundledBodyBudget = (\n",
		}, want: invariantstest.Fails},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invariantstest.Expect(t, invariants.Evaluate(invariantstest.Fixture(t, tc.files), tc.rule), tc.want, tc.violations)
		})
	}
}

const seededKick = "coordinator-roots-changed"

// On the coordinator kick row, a guidance template, served through the
// checkout's prompt overlay, that names a tool no coordinator surface offers
// invariantstest.Fails on every surface; the stock template and a rewording of it that keeps
// its names pass.
func TestPromptRenderReachFailsOnASeededTemplate(t *testing.T) {
	t.Parallel()
	kicks := invariantstest.Rule(t, promptrender.Family, "coordinator-kicks-name-only-offered-tools")
	stockKick, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), guidanceDir+seededKick+".md"))
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
		want    invariantstest.Outcome
		names   []string
	}{
		"stock template":  {want: invariantstest.Passes},
		"reworded":        {overlay: reworded, want: invariantstest.Passes},
		"seeded tool":     {overlay: seeded, want: invariantstest.Violates, names: []string{"complete_leg"}},
		"seeded argument": {overlay: staleArg, want: invariantstest.Violates, names: []string{"wait_ms", "ask_user"}},
		"own argument":    {overlay: ownArg, want: invariantstest.Passes},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := invariantstest.With(anchors, map[string]string{overlay: tc.overlay})
			res := invariants.Evaluate(invariantstest.Fixture(t, files), kicks)
			invariantstest.Expect(t, res, tc.want, 0)
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

// A skill is admitted only when the tools it names are held, and the
// read-only command projection drops the fields a mutating runner needs.
func TestSkillAdmissionFollowsHeldToolsAndProjectedSchemas(t *testing.T) {
	t.Parallel()
	s, err := promptrender.LoadStock(t.Context())
	contractcheck.FailErr(t, "LoadStock", err)

	cmdSkill := skills.Parse("command-skill", []byte(
		"---\nname: command-skill\ndescription: Names command.\n---\nRun it in `command`.\n",
	)).Skill
	if skillOffered(t, cmdSkill, map[string]bool{"recall": true}) {
		t.Fatal("skill naming `command` admitted without `command` held")
	}
	readOnlyCommandHeld := map[string]bool{"command": true}
	if toolcontract.CommandLanguageReachable("curl", readOnlyCommandHeld) || toolcontract.CommandLanguageReachable("git commit", readOnlyCommandHeld) {
		t.Fatal("read-only command held set reached curl or git commit")
	}
	cmdMeta, ok := s.Schemas.ToolMeta("command")
	if !ok {
		t.Fatal("missing command schema")
	}
	roCmdSchema := toolschema.PruneCommandReadOnlySchema(cmdMeta.ArgsSchema)
	for _, prunedField := range []string{"capability_request", "capability_request.host_resources", "secret_use", "socks_proxy", "snapshot_capture", "terminal_capture", "verification"} {
		if toolschema.SchemaAcceptsField(roCmdSchema, prunedField) {
			t.Fatalf("read-only projected command schema accepted %q", prunedField)
		}
	}

	verifySkill := skills.Parse("verify-skill", []byte(
		"---\nname: verify-skill\ndescription: Names verify.\n---\nCall `verify`.\n",
	)).Skill
	if skillOffered(t, verifySkill, readOnlyCommandHeld) {
		t.Fatal("skill naming `verify` admitted without `verify` held")
	}
	if !skillOffered(t, verifySkill, map[string]bool{"verify": true}) {
		t.Fatal("skill naming `verify` rejected with `verify` held")
	}
	verifyMeta, ok := s.Schemas.ToolMeta("verify")
	if !ok {
		t.Fatal("missing verify schema")
	}
	projVerify := toolschema.ProjectToolArgsSchema("verify", verifyMeta.ArgsSchema, toolschema.ProjectOptions{})
	for _, keptField := range []string{"capability_request", "capability_request.host_resources", "secret_use", "socks_proxy"} {
		if !toolschema.SchemaAcceptsField(projVerify, keptField) {
			t.Fatalf("projected verify schema missing %q", keptField)
		}
	}
}

// skillOffered reports whether a declared Reader holding held admits sk.
func skillOffered(t *testing.T, sk skills.Skill, held map[string]bool) bool {
	t.Helper()
	reader, err := toolvocabtest.ReaderHolding(held)
	contractcheck.FailErr(t, "reader holding tools", err)
	return toolvocab.SkillToolsOfferedForReader(sk, reader)
}
