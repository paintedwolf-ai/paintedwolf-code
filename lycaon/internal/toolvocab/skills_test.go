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
	fsys := fstest.MapFS{resource: {Data: []byte("Use `grep` to locate the definition.")}}
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
			if err != nil && !strings.Contains(err.Error(), "grep") {
				t.Fatalf("missing procedure tool in error: %v", err)
			}
		})
	}
}
