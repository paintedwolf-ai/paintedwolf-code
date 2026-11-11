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
		{ID: "explore_readonly", Tools: map[string]bool{"recall": true, "source_history": true}},
		{ID: "acme_narrow", Tools: map[string]bool{"recall": true}},
	}
}

func readRuleNamingGrep(t *testing.T) *oar.RuleSet {
	t.Helper()
	return oar.NewRuleSet([]*oar.Rule{{
		ID:        "READ_TOO_BIG",
		Anchor:    oar.AnchorToolHandler,
		Selector:  oar.Selector{"tool": {"recall"}},
		Fix:       "Use `source_history` to find the region first.",
		Copy:      oar.Copy{Fix: "Use `source_history` to find the region first."},
		NextSteps: &oar.NextSteps{Mentions: []string{"source_history"}},
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
		{ID: "reader", Tools: map[string]bool{"recall": true}},
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
	for _, remedy := range []string{"Call `source_history`.", "Call `source_history(pattern=\"needle\")`.", "Use `source_history (pattern=…)` then inspect the result."} {
		rules := oar.NewRuleSet([]*oar.Rule{{ID: "RECOVERY", Anchor: oar.AnchorToolRejected,
			Selector: oar.Selector{"tool": {"recall"}}, Fix: remedy, Copy: oar.Copy{Fix: remedy}}})
		if _, err := toolvocab.CheckRules(catalog, surfaces(), rules); err == nil || !strings.Contains(err.Error(), "acme_narrow") {
			t.Errorf("unreachable call %q was not rejected: %v", remedy, err)
		}
	}
}

func TestRejectionAnchorRecomputesRecoveryAudience(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "create catalog", err)
	rule := &oar.Rule{ID: "RECOVERY", Anchor: oar.AnchorCoordinatorPreInvoke,
		Fix: "Use `source_history`.", Copy: oar.Copy{Fix: "Use `source_history`."},
		NextSteps: &oar.NextSteps{Mentions: []string{"source_history"}}}
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

func TestBundledCopyNamesAToolOnlyAsSubjectMentionOrCheckedStep(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{Tools: map[string]toolschema.Entry{
		"recall": {Schema: map[string]any{"type": "object", "required": []any{"query"}, "properties": map[string]any{"query": map[string]any{"type": "string"}}}},
	}}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "create catalog", err)
	check := func(rule *oar.Rule) error {
		rule.ID, rule.Audience = "R", []string{"explore_readonly"}
		if rule.Anchor == "" {
			rule.Anchor = oar.AnchorToolRejected
		}
		_, err := toolvocab.CheckRules(catalog, surfaces(), oar.NewRuleSet([]*oar.Rule{rule}))
		return err
	}
	for name, rule := range map[string]*oar.Rule{
		"guarded remedy":        {Copy: oar.Copy{Fix: "{% if paintedwolf.x %}Call `recall`.{% endif %}"}},
		"bare remedy":           {Copy: oar.Copy{Why: "Use source_history first."}},
		"retired name":          {Copy: oar.Copy{Fix: "Call `list_dir`."}},
		"post-invoke operation": {Anchor: oar.AnchorToolPost, Selector: oar.Selector{"paintedwolf.operation": {"list_dir"}}},
		"subject call in fix":   {Selector: oar.Selector{"tool": {"recall"}}, Copy: oar.Copy{Fix: "Retry `recall(query=\"x\")`."}},
		"retired next step":     {NextSteps: &oar.NextSteps{Steps: []oar.NextStep{{Tool: "list_dir", Say: "{{ call }}"}}}},
		"undeclared argument":   {NextSteps: &oar.NextSteps{Steps: []oar.NextStep{{Tool: "recall", Args: map[string]any{"q": "x"}, Say: "{{ call }}"}}}},
		"incomplete call":       {NextSteps: &oar.NextSteps{Steps: []oar.NextStep{{Tool: "recall", Complete: true, Say: "{{ call }}"}}}},
	} {
		if err := check(rule); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, rule := range map[string]*oar.Rule{
		"subject":   {Selector: oar.Selector{"tool": {"recall"}}, Copy: oar.Copy{What: "`recall` failed."}},
		"mention":   {NextSteps: &oar.NextSteps{Mentions: []string{"recall"}}, Copy: oar.Copy{Why: "Results from `recall` are retained."}},
		"fact name": {Copy: oar.Copy{Fix: "See {{ paintedwolf.source_history }}."}},
		"next step": {NextSteps: &oar.NextSteps{Steps: []oar.NextStep{{Tool: "recall", Complete: true, Args: map[string]any{"query": "x"}, Say: "{{ call }}"}}}},
	} {
		if err := check(rule); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A deny that names a native operation stays live: command programs consult it.
func TestProfileDenyMayNameANativeOperation(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "create catalog", err)
	valid := []sandbox.ToolProfile{{ID: "reader", DenyTools: []string{"write", "git_*"}}}
	if err := toolvocab.ValidateProfiles(catalog, valid); err != nil {
		t.Fatalf("operation deny refused: %v", err)
	}
	unknown := []sandbox.ToolProfile{{ID: "reader", DenyTools: []string{"no_such_operation"}}}
	if err := toolvocab.ValidateProfiles(catalog, unknown); err == nil {
		t.Fatal("a deny that matches nothing was accepted")
	}
	if catalog.KnownName("write") {
		t.Fatal("a native operation resolved as a tool")
	}
}
