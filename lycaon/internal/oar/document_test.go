package oar

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCatalogueTiersAreComplete(t *testing.T) {
	testutil.FailErr(t, "validate catalogue tiers", ValidateCatalogueTiers())

	byName := map[string]FactInfo{}
	for _, f := range FactCatalogue() {
		byName[f.Name] = f
		switch f.Tier {
		case FactTierCore, FactTierStandard:
			if f.PublishedName != "" {
				t.Errorf("fact %q is %s tier but carries published name %q", f.Name, f.Tier, f.PublishedName)
			}
		case FactTierHost:
			if f.PublishedName != oarcopy.HostFactNamespace+"."+f.Name {
				t.Errorf("host fact %q published name = %q", f.Name, f.PublishedName)
			}
		default:
			t.Errorf("fact %q has tier %q", f.Name, f.Tier)
		}
	}
	for _, name := range requiredCoreFacts {
		if byName[name].Tier != FactTierCore {
			t.Errorf("core fact %q is tiered %q", name, byName[name].Tier)
		}
	}
	for _, profile := range StandardProfiles() {
		for _, member := range StandardProfileMembers(profile) {
			info, ok := byName[member]
			if !ok {
				continue // observation functions are checked by ValidateCatalogueTiers
			}
			if info.Tier != FactTierStandard || info.Profile != profile {
				t.Errorf("profile %q member %q is tier %q profile %q", profile, member, info.Tier, info.Profile)
			}
		}
	}
}

func TestValidateCatalogueTiersCatchesDrift(t *testing.T) {
	t.Run("untiered", func(t *testing.T) {
		saved := factDecls
		factDecls = append(append([]factDecl(nil), saved...), factDecl{name: "ghost_fact"})
		t.Cleanup(func() { factDecls = saved })
		err := ValidateCatalogueTiers()
		if err == nil || !strings.Contains(err.Error(), `fact "ghost_fact" has no tier`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("core_fact_removed", func(t *testing.T) {
		saved := factDecls
		var kept []factDecl
		for _, d := range saved {
			if d.name != "breaker_count" {
				kept = append(kept, d)
			}
		}
		factDecls = kept
		t.Cleanup(func() { factDecls = saved })
		err := ValidateCatalogueTiers()
		if err == nil || !strings.Contains(err.Error(), `core fact "breaker_count" is not in FactCatalogue()`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("profile_member_removed", func(t *testing.T) {
		// Claimed profiles require every member ([OAR-FACT-16]).
		saved := factDecls
		var kept []factDecl
		for _, d := range saved {
			if d.name != "policy_denied" {
				kept = append(kept, d)
			}
		}
		factDecls = kept
		t.Cleanup(func() { factDecls = saved })
		err := ValidateCatalogueTiers()
		if err == nil || !strings.Contains(err.Error(), `standard profile "tool" member "policy_denied" is not in FactCatalogue()`) {
			t.Fatalf("err = %v", err)
		}
	})
}

// ruleDoc builds a minimal Open Agent Rules 1.0 document for loader tests.
func ruleDoc(t *testing.T, overrides map[string]any) map[string]any {
	t.Helper()
	doc := map[string]any{
		"oar":                "1.0",
		"id":                 "SAMPLE_RULE",
		"kind":               "policy",
		"anchor":             "tool.pre_invoke",
		"effect":             "block",
		"x-paintedwolf-emit": "guard:sample",
		"when":               `tool == "read"`,
		"requires":           map[string]any{"profiles": []any{"tool"}},
	}
	for k, v := range overrides {
		if v == nil {
			delete(doc, k)
			continue
		}
		doc[k] = v
	}
	return doc
}

func testLoader(t *testing.T) *Loader {
	t.Helper()
	ensureCatalog(t)
	l, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "new loader", err)
	return l
}

func TestRequiresFactsRejectedAtLoad(t *testing.T) {
	l := testLoader(t)
	_, _, err := l.parseRule("SAMPLE_RULE", ruleDoc(t, map[string]any{
		"requires": map[string]any{"profiles": []any{"tool"}, "facts": []any{"ghost_fact"}},
	}))
	if err == nil {
		t.Fatal("expected a load error")
	}
	want := `ghost_fact`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
	if _, _, err := l.parseRule("SAMPLE_RULE", ruleDoc(t, map[string]any{
		"requires": map[string]any{"profiles": []any{"tool"}, "facts": []any{"paintedwolf.surface"}},
	})); err != nil {
		t.Fatalf("provided facts should load: %v", err)
	}
	// A profile this host does not provide is named in the error too.
	_, _, err = l.parseRule("SAMPLE_RULE", ruleDoc(t, map[string]any{
		"requires": map[string]any{"profiles": []any{"tool", "telepathy"}},
	}))
	if err == nil || !strings.Contains(err.Error(), `requires capability profile "telepathy"`) {
		t.Fatalf("err = %v", err)
	}
}

// Rule identity includes its namespace; rendered codes use the bare ID.
func TestNamespaceIdentity(t *testing.T) {
	l := testLoader(t)
	a, _, err := l.parseRule("SHARED_CODE", ruleDoc(t, map[string]any{"id": "SHARED_CODE", "namespace": "acme.security"}))
	testutil.FailErr(t, "parse namespaced rule", err)
	b, _, err := l.parseRule("SHARED_CODE", ruleDoc(t, map[string]any{"id": "SHARED_CODE", "namespace": "globex.policy"}))
	testutil.FailErr(t, "parse second namespaced rule", err)

	merged, err := MergeTiered(NewRuleSet([]*Rule{a}), NewRuleSet([]*Rule{b}))
	testutil.FailErr(t, "merge distinct identities", err)
	if merged.Len() != 2 {
		t.Fatalf("merged %d rules, want 2", merged.Len())
	}
	for _, r := range merged.All() {
		if r.ID != "SHARED_CODE" {
			t.Fatalf("rendered code = %q, want the bare id", r.ID)
		}
	}

	c, _, err := l.parseRule("SHARED_CODE", ruleDoc(t, map[string]any{"id": "SHARED_CODE"}))
	testutil.FailErr(t, "parse unnamespaced rule", err)
	d, _, err := l.parseRule("SHARED_CODE", ruleDoc(t, map[string]any{"id": "SHARED_CODE", "when": `tool == "grep"`}))
	testutil.FailErr(t, "parse duplicate rule", err)
	if _, err := MergeTiered(NewRuleSet([]*Rule{c}), NewRuleSet([]*Rule{d})); err == nil {
		t.Fatal("same identity in the same tier must still fail load")
	}

	if _, _, err := l.parseRule("SAMPLE_RULE", ruleDoc(t, map[string]any{"namespace": "Not Valid"})); err == nil {
		t.Fatal("expected an invalid namespace to be rejected")
	}
}

// TestOARVersionWindow accepts only the supported version.
func TestOARVersionWindow(t *testing.T) {
	l := testLoader(t)
	if _, _, err := l.parseRule("SAMPLE_RULE", ruleDoc(t, map[string]any{"oar": "1.0"})); err != nil {
		t.Fatalf("oar 1.0 should load: %v", err)
	}
	for _, v := range []string{"2.0", "1.1", "draft"} {
		if _, _, err := l.parseRule("SAMPLE_RULE", ruleDoc(t, map[string]any{"oar": v})); err == nil {
			t.Fatalf("expected oar %s to be rejected", v)
		}
	}
	// Unmarked documents are not OAR rules.
	_, skip, err := l.parseRule("SAMPLE_RULE", ruleDoc(t, map[string]any{"oar": nil}))
	testutil.FailErr(t, "parse unmarked document", err)
	if !skip {
		t.Fatal("unmarked document was loaded as an OAR rule")
	}
}

func TestDocumentWorldIsClosed(t *testing.T) {
	l := testLoader(t)
	raw, err := json.Marshal(ruleDoc(t, map[string]any{"branch_instruction": "nope"}))
	testutil.FailErr(t, "marshal", err)
	err = l.ValidateDocument(raw)
	if err == nil {
		t.Fatal("expected an unknown key to be rejected")
	}
	if !strings.Contains(err.Error(), "branch_instruction") {
		t.Fatalf("error %q does not name the offending key", err)
	}

	raw, err = json.Marshal(ruleDoc(t, map[string]any{"x-vendor-note": "fine"}))
	testutil.FailErr(t, "marshal", err)
	if err := l.ValidateDocument(raw); err != nil {
		t.Fatalf("x- extension should pass: %v", err)
	}
}

// Presentation copy is excluded from decision identity ([OAR-DOC-24]).
func TestCopyDoesNotAffectTheDecision(t *testing.T) {
	l := testLoader(t)
	var got []string
	for _, what := range []string{"one wording", "a completely different wording"} {
		r, _, err := l.parseRule("COPY_PROBE", ruleDoc(t, map[string]any{
			"id":   "COPY_PROBE",
			"copy": map[string]any{"what": what, "fix": what},
		}))
		testutil.FailErr(t, "parse "+what, err)
		p := NewGuardPipeline(NewRuleSet([]*Rule{r}), l, NewCounterStore())
		p.EnableAnchor("tool.pre_invoke")
		res, err := p.EvaluateBlock(context.Background(), "tool.pre_invoke", factsToContext(map[string]any{"tool": "read"}))
		testutil.FailErr(t, "evaluate "+what, err)
		if res.Decision == nil {
			t.Fatalf("copy %q produced no decision", what)
		}
		got = append(got, string(res.Decision.Effect)+"/"+res.Decision.Code)
	}
	if got[0] != got[1] {
		t.Fatalf("copy changed the decision: %q vs %q", got[0], got[1])
	}
}

// Portability findings do not fail rule loading.
func TestLintPortability(t *testing.T) {
	l := testLoader(t)
	p, err := LoadCapabilityDocument(hostCapabilityDocumentPath(t))
	testutil.FailErr(t, "load host profile", err)
	prev := InstalledCapabilityDocument()
	InstallCapabilityDocument(p)
	t.Cleanup(func() { InstallCapabilityDocument(prev) })

	core, _, err := l.parseRule("PORTABLE_RULE", ruleDoc(t, map[string]any{
		"id": "PORTABLE_RULE", "anchor": "agent.post_turn",
		"when":     `policy_denied && tool == "read"`,
		"requires": map[string]any{"profiles": []any{"tool"}},
	}))
	testutil.FailErr(t, "parse portable rule", err)
	if rep := LintPortability(core); !rep.Portable {
		t.Fatalf("core-only rule reported non-portable: %+v", rep)
	}

	host, _, err := l.parseRule("HOST_RULE", ruleDoc(t, map[string]any{
		"id": "HOST_RULE", "anchor": "session.pre_invoke",
		"when":     "paintedwolf.pending_overlay_promote",
		"requires": map[string]any{"facts": []any{"paintedwolf.pending_overlay_promote"}},
	}))
	testutil.FailErr(t, "parse host rule", err)
	rep := LintPortability(host)
	if rep.Portable {
		t.Fatal("host rule reported portable")
	}
	if rep.HostAnchor != "session.pre_invoke" {
		t.Errorf("HostAnchor = %q", rep.HostAnchor)
	}
	if len(rep.HostFacts) != 1 || rep.HostFacts[0] != "paintedwolf.pending_overlay_promote" {
		t.Errorf("HostFacts = %v", rep.HostFacts)
	}
}
