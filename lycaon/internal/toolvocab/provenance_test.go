package toolvocab_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

// readOnlyProfiles pairs a bundled profile holding the remedy with a pack
// profile that does not.
func readOnlyProfiles() []sandbox.ToolProfile {
	return []sandbox.ToolProfile{
		{ID: "explore_readonly", Tools: map[string]bool{"read": true, "grep": true}},
		{ID: "acme_narrow", Tools: map[string]bool{"read": true}},
	}
}

func readRuleNamingGrep(t *testing.T) *oar.RuleSet {
	t.Helper()
	return oar.NewRuleSet([]*oar.Rule{{
		ID:       "READ_TOO_BIG",
		Anchor:   oar.AnchorToolHandler,
		Selector: oar.Selector{"tool": {"read"}},
		Fix:      "Use `grep` to find the region first.",
		Copy:     oar.Copy{Fix: "Use `grep` to find the region first."},
	}})
}

func surfaces() toolvocab.Surfaces {
	return toolvocab.Surfaces{Coordinator: "explore_readonly", Workers: []string{"explore_readonly", "acme_narrow"}}
}

// A pack profile narrower than a bundled rule's remedy is an observation, not
// a defect: a narrow profile is what the unit kind is for.
func TestARemedyUnreachableFromAnotherAuthorsProfileIsANote(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(
		&toolschema.Config{}, readOnlyProfiles(),
		toolvocab.Provenance{
			ToolProfile: func(id string) string {
				if id == "acme_narrow" {
					return "acme/kit"
				}
				return ""
			},
		})
	testutil.FailErr(t, "NewCatalog", err)

	notes, err := toolvocab.CheckRules(catalog, surfaces(), readRuleNamingGrep(t))
	testutil.FailErr(t, "CheckRules", err)
	if len(notes) != 1 || !strings.Contains(notes[0], "acme_narrow") {
		t.Fatalf("want one note naming the pack profile, got %v", notes)
	}
}

// Against a profile the rule's own author wrote it is still an error.
func TestARemedyUnreachableFromItsOwnAuthorsProfileIsAnError(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "NewCatalog", err)

	notes, err := toolvocab.CheckRules(catalog, surfaces(), readRuleNamingGrep(t))
	if err == nil {
		t.Fatalf("want an error for a same-authority unreachable remedy, notes=%v", notes)
	}
	if !strings.Contains(err.Error(), "acme_narrow") {
		t.Fatalf("error should name the profile: %v", err)
	}
}

// A schema unit describes a tool the host has; it cannot add one.
func TestAPackSchemaMustNameARegisteredTool(t *testing.T) {
	schemas := &toolschema.Config{Tools: map[string]toolschema.Entry{"no_such_tool": {Description: "ghost"}}}
	_, err := toolvocab.NewCatalog(schemas, readOnlyProfiles(), toolvocab.Provenance{
		ToolSchema: func(string) string { return "acme/kit" },
	})
	if err == nil || !strings.Contains(err.Error(), "no_such_tool") {
		t.Fatalf("want a refusal naming the tool, got %v", err)
	}
}

// A bundled schema still declares its tool; nothing else registers it.
func TestABundledSchemaDeclaresItsTool(t *testing.T) {
	schemas := &toolschema.Config{Tools: map[string]toolschema.Entry{"host_only_tool": {Description: "bundled"}}}
	catalog, err := toolvocab.NewCatalog(schemas, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "NewCatalog", err)
	if !catalog.KnownName("host_only_tool") {
		t.Fatal("a bundled schema declares the tool exists")
	}
}

func TestCredentialAssignmentAudienceFollowsInvokingTool(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, []sandbox.ToolProfile{
		{ID: "coordinator", Tools: map[string]bool{"write": true}},
		{ID: "editor", Tools: map[string]bool{"write": true}},
		{ID: "reader", Tools: map[string]bool{"read": true}},
	}, toolvocab.Provenance{})
	testutil.FailErr(t, "create catalog", err)
	audience, err := toolvocab.Audience(catalog, toolvocab.Surfaces{Coordinator: "coordinator", Workers: []string{"reader"}}, &oar.Rule{
		ID: "ASSIGNMENT", Anchor: oar.AnchorCredentialAssignment, Selector: oar.Selector{"tool": {"write"}},
	})
	testutil.FailErr(t, "resolve assignment audience", err)
	if strings.Join(audience, ",") != "coordinator,editor" {
		t.Fatalf("assignment audience = %v, want coordinator and editor", audience)
	}
}

func TestRecoveryReachabilityIncludesCallExamples(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "create catalog", err)
	for _, remedy := range []string{"Call `grep`.", "Call `grep(pattern=\"needle\")`.", "Use `grep (pattern=…)` then inspect the result."} {
		rules := oar.NewRuleSet([]*oar.Rule{{ID: "RECOVERY", Anchor: oar.AnchorToolRejected,
			Selector: oar.Selector{"tool": {"read"}}, Fix: remedy, Copy: oar.Copy{Fix: remedy}}})
		if _, err := toolvocab.CheckRules(catalog, surfaces(), rules); err == nil || !strings.Contains(err.Error(), "acme_narrow") {
			t.Errorf("unreachable call %q was not rejected: %v", remedy, err)
		}
	}
}

func TestRejectionAnchorRecomputesRecoveryAudience(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "create catalog", err)
	rule := &oar.Rule{ID: "RECOVERY", Anchor: oar.AnchorCoordinatorPreInvoke,
		Fix: "Use `grep`.", Copy: oar.Copy{Fix: "Use `grep`."}}
	_, err = toolvocab.CheckRules(catalog, surfaces(), oar.NewRuleSet([]*oar.Rule{rule}))
	testutil.FailErr(t, "coordinator recovery", err)
	rule.Anchor = oar.AnchorToolRejected
	if _, err := toolvocab.CheckRules(catalog, surfaces(), oar.NewRuleSet([]*oar.Rule{rule})); err == nil {
		t.Fatal("moving a rule to the shared rejection anchor silently retained its coordinator audience")
	}
	rule.Audience = []string{"explore_readonly"}
	_, err = toolvocab.CheckRules(catalog, surfaces(), oar.NewRuleSet([]*oar.Rule{rule}))
	testutil.FailErr(t, "explicit producer audience", err)
}
