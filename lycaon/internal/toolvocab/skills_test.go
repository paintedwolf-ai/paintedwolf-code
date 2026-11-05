package toolvocab_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

func TestSkillProcedureToolsMustBeReachable(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "build tool catalog", err)
	const resource = "references/search.md"
	fsys := fstest.MapFS{resource: {Data: []byte("Use `source_history` to locate the definition.")}}
	sk := skills.Skill{Name: "search", Body: "Select a reference.", Metadata: map[string]string{skills.TemplateResourcesMetadata: resource}}
	sk.BindResources(fsys, ".", skills.DiscoverBundledResources(fsys, "."), "")
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

func TestSkillToolsOfferedGatesStockSkillsByNonOptionalTools(t *testing.T) {
	stock := skills.Skill{
		Name:          "review-accessibility",
		Body:          "Use `measure_page` and `page_open` to inspect the page, and optionally `capture_page`.",
		OptionalTools: []string{"capture_page"},
	}
	if toolvocab.SkillToolsOffered(stock, map[string]bool{"measure_page": true}) {
		t.Fatal("stock skill with missing required tool was accepted")
	}
	if !toolvocab.SkillToolsOffered(stock, map[string]bool{"measure_page": true, "page_open": true}) {
		t.Fatal("stock skill with all required tools offered was rejected when optional tool was absent")
	}
	userSkill := stock
	userSkill.UserProvided = true
	if !toolvocab.SkillToolsOffered(userSkill, nil) {
		t.Fatal("user-provided skill was restricted by offered tools")
	}
}
