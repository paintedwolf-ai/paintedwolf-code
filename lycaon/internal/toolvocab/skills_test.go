package toolvocab_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

func TestSkillProcedureToolsMustBeReachable(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "build tool catalog", err)
	sk := skills.Skill{Name: "search", Body: "Use `source_history` to locate the definition."}
	for _, tc := range []struct {
		profile   string
		wantError bool
	}{{"explore_readonly", false}, {"acme_narrow", true}} {
		t.Run(tc.profile, func(t *testing.T) {
			err := toolvocab.ValidateSkills(catalog, []toolvocab.AgentSurface{{AgentID: "reader", ProfileID: tc.profile, Skills: skills.Selector{All: true}}}, []skills.Skill{sk})
			if (err != nil) != tc.wantError {
				t.Fatalf("profile=%s: %v", tc.profile, err)
			}
			if err != nil && !strings.Contains(err.Error(), "source_history") {
				t.Fatalf("missing procedure tool in error: %v", err)
			}
		})
	}
}

func TestSkillProcedureRejectsRetiredOperationNames(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "build tool catalog", err)
	for _, retired := range []string{"list_dir", "grep", "read", "write", "edit", "replace_lines"} {
		t.Run(retired, func(t *testing.T) {
			sk := skills.Skill{Name: "retired-" + retired, Body: "Run `" + retired + "` to inspect the tree."}
			err := toolvocab.ValidateSkills(catalog, []toolvocab.AgentSurface{{AgentID: "reader", ProfileID: "explore_readonly", Skills: skills.Selector{All: true}}}, []skills.Skill{sk})
			if err == nil || !strings.Contains(err.Error(), retired) {
				t.Fatalf("retired operation %q in skill body was not rejected: %v", retired, err)
			}
		})
	}
}

func TestSkillToolsOfferedGatesStockSkillsByRenderedBody(t *testing.T) {
	stock := skills.Skill{
		Name: "review-accessibility",
		Body: "Use `measure_page` and `page_open` to inspect the page, and optionally {% if profile_has_capture_page %}`capture_page`{% endif %}.",
	}
	if toolvocab.SkillToolsOffered(stock, map[string]bool{"measure_page": true}) {
		t.Fatal("stock skill with missing required tool was accepted")
	}
	if !toolvocab.SkillToolsOffered(stock, map[string]bool{"measure_page": true, "page_open": true}) {
		t.Fatal("stock skill with all required tools offered was rejected")
	}
	if !toolvocab.SkillToolsOffered(stock, map[string]bool{"measure_page": true, "page_open": true, "capture_page": true}) {
		t.Fatal("stock skill with the guarded tool offered was rejected")
	}
	// Without the guard the mention is a hard requirement: the retired
	// optional-tool escape hatch is gone, so an unguarded name must gate the skill.
	unguarded := stock
	unguarded.Body = "Use `measure_page` and `page_open` to inspect the page, and optionally `capture_page`."
	if toolvocab.SkillToolsOffered(unguarded, map[string]bool{"measure_page": true, "page_open": true}) {
		t.Fatal("unguarded tool mention escaped the offered-tools gate")
	}
	userSkill := stock
	userSkill.UserProvided = true
	if !toolvocab.SkillToolsOffered(userSkill, nil) {
		t.Fatal("user-provided skill was restricted by offered tools")
	}
}

func TestSkillToolsOfferedResolvesCoordinatorBranches(t *testing.T) {
	stock := skills.Skill{
		Name: "split-audit",
		Body: "{% if is_coordinator %}Call `measure_page` for the audit.{% else %}Call `capture_page` to snapshot.{% endif %}",
	}
	if !toolvocab.SkillToolsOffered(stock, map[string]bool{"task": true, "measure_page": true}) {
		t.Fatal("coordinator branch with offered tools was rejected")
	}
	if toolvocab.SkillToolsOffered(stock, map[string]bool{"task": true}) {
		t.Fatal("coordinator branch naming an unoffered tool was accepted")
	}
	if !toolvocab.SkillToolsOffered(stock, map[string]bool{"capture_page": true}) {
		t.Fatal("worker branch with offered tools was rejected")
	}
	if toolvocab.SkillToolsOffered(stock, map[string]bool{}) {
		t.Fatal("worker branch naming an unoffered tool was accepted")
	}
}

func TestSkillToolsOfferedAnyOfToolsDisjunction(t *testing.T) {
	stock := skills.Skill{
		Name: "mutate-a-tree",
		Body: "Apply the change with `code_rewrite` or `jq_edit`.",
		Metadata: map[string]string{
			"paintedwolf.any_of_tools": "code_rewrite, jq_edit",
		},
	}
	if toolvocab.SkillToolsOffered(stock, map[string]bool{"command": true}) {
		t.Fatal("disjunctive skill with no any_of member offered was accepted")
	}
	if !toolvocab.SkillToolsOffered(stock, map[string]bool{"command": true, "jq_edit": true}) {
		t.Fatal("disjunctive skill with one any_of member offered was rejected")
	}
	// any_of members are individually optional in the body scan: one offered
	// member admits the skill even though the other is absent.
	if !toolvocab.SkillToolsOffered(stock, map[string]bool{"code_rewrite": true}) {
		t.Fatal("disjunctive skill admitted by its only offered member was rejected")
	}
	empty := stock
	empty.Metadata = nil
	if toolvocab.SkillToolsOffered(empty, map[string]bool{"command": true}) {
		t.Fatal("skill without any_of metadata escaped the body-scan requirement")
	}
	// The disjunction applies even when the body does not name the members.
	unmentioned := skills.Skill{
		Name: "delegate-or-wait",
		Body: "Hand the work off or wait for the person.",
		Metadata: map[string]string{
			"paintedwolf.any_of_tools": "task, wait",
		},
	}
	if toolvocab.SkillToolsOffered(unmentioned, map[string]bool{"command": true}) {
		t.Fatal("disjunctive requirement was ignored for unmentioned members")
	}
	if !toolvocab.SkillToolsOffered(unmentioned, map[string]bool{"command": true, "wait": true}) {
		t.Fatal("offered any_of member was not honored for unmentioned members")
	}
}

// skillWithResource binds one references/*.md resource to a stock skill so
// MarkdownResources and ReadResource resolve without a filesystem on disk.
func skillWithResource(body, resource string) skills.Skill {
	fsys := fstest.MapFS{
		"pack/skills/demo/references/usage.md": &fstest.MapFile{Data: []byte(resource)},
	}
	sk := skills.Skill{Name: "demo", Body: body}
	sk.BindResources(fsys, "pack/skills/demo", skills.DiscoverBundledResources(fsys, "pack/skills/demo"), "")
	return sk
}

func validateWith(t *testing.T, catalog *toolvocab.Catalog, profile string, sk skills.Skill) error {
	t.Helper()
	return toolvocab.ValidateSkills(catalog, []toolvocab.AgentSurface{
		{AgentID: "reader", ProfileID: profile, Skills: skills.Selector{All: true}},
	}, []skills.Skill{sk})
}

func TestSkillResourceMentionsMustBeReachable(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "build tool catalog", err)
	unguarded := skillWithResource(
		"Read the overview first.",
		"Then use `capture_page` to snapshot the page.",
	)
	err = validateWith(t, catalog, "explore_readonly", unguarded)
	if err == nil || !strings.Contains(err.Error(), "capture_page") || !strings.Contains(err.Error(), "references/usage.md") {
		t.Fatalf("unreachable tool in skill resource was not rejected: %v", err)
	}
	guarded := skillWithResource(
		"Read the overview first.",
		"{% if profile_has_capture_page %}Then use `capture_page` to snapshot the page.{% endif %}",
	)
	if err := validateWith(t, catalog, "explore_readonly", guarded); err != nil {
		t.Fatalf("guarded resource mention was rejected: %v", err)
	}
	if err := validateWith(t, catalog, "acme_narrow", guarded); err != nil {
		t.Fatalf("guarded resource mention was rejected for a narrower profile: %v", err)
	}
}

func TestSkillGuardedMentionsResolvePerReader(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "build tool catalog", err)
	guarded := skills.Skill{
		Name: "snapshot",
		Body: "{% if profile_has_capture_page %}Use `capture_page` for the snapshot.{% endif %}",
	}
	if err := validateWith(t, catalog, "explore_readonly", guarded); err != nil {
		t.Fatalf("guarded mention was rejected for a reader without the tool: %v", err)
	}
	unguarded := guarded
	unguarded.Body = "Use `capture_page` for the snapshot."
	err = validateWith(t, catalog, "explore_readonly", unguarded)
	if err == nil || !strings.Contains(err.Error(), "capture_page") {
		t.Fatalf("unguarded mention was not rejected: %v", err)
	}
	if !strings.Contains(err.Error(), "{% if profile_has_capture_page %}") {
		t.Fatalf("error does not point at the guard convention: %v", err)
	}
}

func TestSkillMutatingCommandFormsNeedCommandMutation(t *testing.T) {
	mutation, err := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	testutil.FailErr(t, "build mutation effects", err)
	readOnly, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	testutil.FailErr(t, "build read-only effects", err)
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, []sandbox.ToolProfile{
		{ID: "mutating", Tools: map[string]bool{"command": true}, CommandEffects: mutation},
		{ID: "readonly", Tools: map[string]bool{"command": true}, CommandEffects: readOnly},
	}, toolvocab.Provenance{})
	testutil.FailErr(t, "build tool catalog", err)
	for _, tc := range []struct {
		profile   string
		wantError bool
	}{{"mutating", false}, {"readonly", true}} {
		for _, form := range []string{"edit", "replace-lines", "jq-edit"} {
			t.Run(tc.profile+"/"+form, func(t *testing.T) {
				sk := skills.Skill{Name: "fixer", Body: "Run `" + form + "` to apply the change."}
				err := validateWith(t, catalog, tc.profile, sk)
				if (err != nil) != tc.wantError {
					t.Fatalf("profile=%s form=%s: %v", tc.profile, form, err)
				}
			})
		}
	}
}

func TestUnreachableNamesCommandForms(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, []sandbox.ToolProfile{{ID: "stock"}}, toolvocab.Provenance{})
	testutil.FailErr(t, "build tool catalog", err)
	corpus := "Run `edit`, `replace-lines`, `repo-map`, `summarize`, and `code_rewrite`."
	for _, tc := range []struct {
		name      string
		reachable map[string]bool
		want      []string
	}{
		{"nothing", nil, []string{"code_rewrite", "edit", "replace-lines", "repo-map", "summarize"}},
		{"command only", map[string]bool{"command": true}, []string{"code_rewrite", "edit", "replace-lines", "repo-map", "summarize"}},
		{"command mutation", map[string]bool{"command": true, toolvocab.ReachCommandMutation: true}, []string{"code_rewrite", "repo-map", "summarize"}},
		{"survey tool", map[string]bool{"command": true, toolvocab.ReachCommandMutation: true, "survey_repo": true}, []string{"code_rewrite", "summarize"}},
		{"rewrite tool", map[string]bool{"command": true, toolvocab.ReachCommandMutation: true, "code_rewrite": true}, []string{"repo-map", "summarize"}},
		{"summarize tool", map[string]bool{"summarize": true}, []string{"code_rewrite", "edit", "replace-lines", "repo-map"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toolvocab.UnreachableNames(catalog, corpus, tc.reachable)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("UnreachableNames = %v, want %v", got, tc.want)
			}
		})
	}
	held := toolvocab.UnreachableNames(catalog, "Use `capture_page` when you need a snapshot.", map[string]bool{"capture_page": true})
	if len(held) != 0 {
		t.Fatalf("held tool reported unreachable: %v", held)
	}
}

func TestSkillRejectsUnknownProfileHasIdentifier(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "build tool catalog", err)
	bogus := skills.Skill{
		Name: "daydream",
		Body: "{% if profile_has_bogus_tool %}Use `recall`.{% endif %}",
	}
	err = validateWith(t, catalog, "explore_readonly", bogus)
	if err == nil || !strings.Contains(err.Error(), "profile_has_bogus_tool") {
		t.Fatalf("unknown profile_has_ identifier was not rejected: %v", err)
	}
	inResource := skillWithResource(
		"Read the overview.",
		"{% if profile_has_bogus_tool %}Then continue.{% endif %}",
	)
	err = validateWith(t, catalog, "explore_readonly", inResource)
	if err == nil || !strings.Contains(err.Error(), "profile_has_bogus_tool") {
		t.Fatalf("unknown identifier in resource was not rejected: %v", err)
	}
	alias := skills.Skill{
		Name: "writer",
		Body: "{% if profile_has_write_tools %}Apply the change with `edit`.{% endif %}",
	}
	if err := validateWith(t, catalog, "explore_readonly", alias); err != nil {
		t.Fatalf("write_tools alias was rejected: %v", err)
	}
}

func TestSkillRejectsUnknownAnyOfToolsMember(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "build tool catalog", err)
	bogus := skills.Skill{
		Name: "either",
		Body: "Ask the person or wait.",
		Metadata: map[string]string{
			"paintedwolf.any_of_tools": "bogus_tool, capture_page",
		},
	}
	err = validateWith(t, catalog, "explore_readonly", bogus)
	if err == nil || !strings.Contains(err.Error(), "bogus_tool") {
		t.Fatalf("unknown any_of_tools member was not rejected: %v", err)
	}
	real := bogus
	real.Metadata = map[string]string{"paintedwolf.any_of_tools": "capture_page"}
	if err := validateWith(t, catalog, "explore_readonly", real); err != nil {
		t.Fatalf("registered any_of_tools member was rejected: %v", err)
	}
}
