package interactions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// A disposable pack whose partial, included by a guidance template, holds one
// guard per control.
var controlPackFiles = map[string]string{
	"guidance/ctl-root.md": `{% include "partials/ctl-guards.md" %}`,
	"shared/partials/ctl-guards.md": "{% if alpha and beta %}A{% endif %}\n" +
		"{% if gamma or delta %}O{% endif %}\n" +
		"{% if first %}F{% elif second and third %}S{% endif %}\n" +
		"{% if has_git and has_any_git_root %}G{% endif %}\n",
}

const (
	siteAnd     = "shared/partials/ctl-guards@L1:C4:if"
	siteOr      = "shared/partials/ctl-guards@L2:C4:if"
	siteElif    = "shared/partials/ctl-guards@L3:C19:elif"
	siteCoupled = "shared/partials/ctl-guards@L4:C4:if"
)

var gitCoupling = AtomConstraint{
	Atom: "has_git", Implies: "has_any_git_root", Recipient: RecipientCoordinator,
	Source: "internal/session/readerfacts/resolver.go:gitFacts: the any-root probe includes the primary root",
}

// controlVerdict is one control's evidence: the ledger check's outcome on the
// faulty input and on its corrected neighbour.
type controlVerdict struct {
	Control   string `json:"control"`
	Fault     string `json:"fault_diagnostic"`
	Corrected string `json:"corrected_diagnostic,omitempty"`
}

// Each fault leaves an atom without an independent witness, and the ledger
// check names the site and atom; its corrected neighbour passes.
func TestConditionLedgerControls(t *testing.T) {
	contractcheck.FailErr(t, "install anchor catalog", anchorcatalog.InstallBundled())
	contractcheck.FailErr(t, "install engine declaration", oar.InstallEngineDeclarationBundled(""))
	fx := newControlFixture(t)
	siteRule := fx.rule.Qualified() + "#when"
	vec := func(kv ...any) map[string]any {
		m := map[string]any{"is_coordinator": true}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	diagonal := []map[string]any{vec("alpha", true, "beta", true), vec("alpha", false, "beta", false)}
	shortCircuit := []map[string]any{vec("gamma", true, "delta", false), vec("gamma", false, "delta", false)}
	bypassed := []map[string]any{vec("first", true, "second", true, "third", true), vec("first", true, "second", false, "third", true)}
	coupled := []map[string]any{
		vec("has_git", true, "has_any_git_root", true), vec("has_git", false, "has_any_git_root", true),
		vec("has_git", false, "has_any_git_root", false),
	}
	askCommand := []occurrence{{"ask", "command"}, {"agent", "read_file"}}

	var verdicts []controlVerdict
	for _, c := range []struct {
		name, site       string
		atoms            []string
		fault, corrected func() *ConditionLedger
	}{
		{
			name: "and_with_only_diagonal_cases_through_an_include", site: siteAnd, atoms: []string{"alpha", "beta"},
			fault: func() *ConditionLedger { return fx.ledger(t, siteAnd, diagonal, nil, nil) },
			corrected: func() *ConditionLedger {
				return fx.ledger(t, siteAnd, append(diagonal, vec("alpha", true, "beta", false), vec("alpha", false, "beta", true)), nil, nil)
			},
		},
		{
			name: "and_with_only_diagonal_cases_through_an_oar_condition", site: siteRule,
			atoms: []string{`paintedwolf.session_mode == "ask"`, `tool == "command"`},
			fault: func() *ConditionLedger { return fx.ledger(t, siteRule, nil, askCommand, nil) },
			corrected: func() *ConditionLedger {
				return fx.ledger(t, siteRule, nil, append(askCommand, occurrence{"ask", "read_file"}, occurrence{"agent", "command"}), nil)
			},
		},
		{
			name: "or_with_short_circuit", site: siteOr, atoms: []string{"delta"},
			fault: func() *ConditionLedger { return fx.ledger(t, siteOr, shortCircuit, nil, nil) },
			corrected: func() *ConditionLedger {
				return fx.ledger(t, siteOr, append(shortCircuit, vec("gamma", false, "delta", true)), nil, nil)
			},
		},
		{
			name: "elif_bypassed_by_a_preceding_arm", site: siteElif, atoms: []string{"second", "third"},
			fault: func() *ConditionLedger { return fx.ledger(t, siteElif, bypassed, nil, nil) },
			corrected: func() *ConditionLedger {
				return fx.ledger(t, siteElif, append(bypassed,
					vec("first", false, "second", true, "third", true), vec("first", false, "second", false, "third", true),
					vec("first", false, "second", true, "third", false)), nil, nil)
			},
		},
		{
			name: "coupled_predicate_without_its_production_constraint", site: siteCoupled, atoms: []string{"has_any_git_root"},
			fault:     func() *ConditionLedger { return fx.ledger(t, siteCoupled, coupled, nil, nil) },
			corrected: func() *ConditionLedger { return fx.ledger(t, siteCoupled, coupled, nil, []AtomConstraint{gitCoupling}) },
		},
		{
			name: "observation_contradicting_a_recorded_constraint", site: siteCoupled, atoms: []string{"has_git ⇒ has_any_git_root"},
			fault: func() *ConditionLedger {
				return fx.ledger(t, siteCoupled, append(coupled, vec("has_git", true, "has_any_git_root", false)), nil, []AtomConstraint{gitCoupling})
			},
			corrected: func() *ConditionLedger { return fx.ledger(t, siteCoupled, coupled, nil, []AtomConstraint{gitCoupling}) },
		},
		{
			name: "only_witness_removed", site: siteAnd, atoms: []string{"alpha"},
			fault: func() *ConditionLedger {
				return fx.ledger(t, siteAnd, append(diagonal, vec("alpha", false, "beta", true)), nil, nil)
			},
			corrected: func() *ConditionLedger {
				return fx.ledger(t, siteAnd, append(diagonal, vec("alpha", false, "beta", true), vec("alpha", true, "beta", false)), nil, nil)
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			faultErr := c.fault().ValidateZeroUnexplainedGaps()
			if faultErr == nil {
				t.Fatal("fault passed the ledger check")
			}
			for _, want := range append([]string{c.site}, c.atoms...) {
				if !strings.Contains(faultErr.Error(), want) {
					t.Errorf("fault diagnostic does not name %q:\n%v", want, faultErr)
				}
			}
			corrected := c.corrected()
			if err := corrected.ValidateZeroUnexplainedGaps(); err != nil {
				t.Fatalf("corrected neighbour failed: %v", err)
			}
			v := controlVerdict{Control: c.name, Fault: faultErr.Error()}
			if len(corrected.Exclusions) > 0 {
				raw, _ := json.Marshal(corrected.Exclusions)
				v.Corrected = "exclusions " + string(raw)
			}
			verdicts = append(verdicts, v)
		})
	}
	raw, _ := json.MarshalIndent(verdicts, "", "  ")
	t.Logf("condition control verdicts:\n%s", raw)
}

type occurrence struct{ mode, tool string }

type controlFixture struct {
	eff       *extpacks.EffectiveCatalog
	templates map[string]string
	sites     []ConditionSite
	rule      *oar.Rule
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "acme-ctl")
	manifest := "manifest_version: 1\nid: acme/ctl\nname: acme/ctl\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\n"
	contractcheck.FailErr(t, "write manifest", os.MkdirAll(root, 0o755))
	contractcheck.FailErr(t, "write manifest", os.WriteFile(filepath.Join(root, "extension.yaml"), []byte(manifest), 0o644))
	for rel, body := range controlPackFiles {
		p := filepath.Join(root, filepath.FromSlash(rel))
		contractcheck.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(p), 0o755))
		contractcheck.FailErr(t, "write "+rel, os.WriteFile(p, []byte(body), 0o644))
	}
	man, err := extpacks.LoadManifest(root)
	contractcheck.FailErr(t, "load manifest", err)
	pc, err := extpacks.InventoryPack(extpacks.Pack{ID: "acme/ctl", Root: extpacks.OnDisk(root)}, man)
	contractcheck.FailErr(t, "inventory pack", err)
	desired := extpacks.EmptyDesired()
	desired.Packs = append(desired.Packs, extpacks.DesiredPack{ID: "acme/ctl"})
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: []extpacks.PackContent{pc}, Desired: desired})

	templates, err := TemplateUnits(eff)
	contractcheck.FailErr(t, "template units", err)
	sites, err := DiscoverConditionSites(eff, nil)
	contractcheck.FailErr(t, "discover condition sites", err)
	rule := &oar.Rule{
		ID: "CTL_AND", Kind: oar.KindPolicy, Anchor: oar.AnchorToolPreInvoke, Effect: oar.EffectBlock,
		When: `paintedwolf.session_mode == "ask" && tool == "command"`,
	}
	ruleSites, err := RuleConditionSites([]*oar.Rule{rule}, func(*oar.Rule) string { return "acme/ctl" })
	contractcheck.FailErr(t, "rule condition sites", err)
	return &controlFixture{eff: eff, templates: templates, sites: append(sites, ruleSites...), rule: rule}
}

// ledger renders the fixture template for each of renders and evaluates the
// fixture rule for each occurrence through the production evaluators with
// atom observation on, then builds the ledger for site alone.
func (fx *controlFixture) ledger(t *testing.T, site string, renders []map[string]any, occurrences []occurrence, constraints []AtomConstraint) *ConditionLedger {
	t.Helper()
	tracker := hoststate.NewCoverageTracker()
	for id, source := range fx.templates {
		contractcheck.FailErr(t, "register "+id, tracker.RegisterTemplate(id, source, pongoplain.Composed))
	}
	tracker.AttributeTemplateRefs(func(ref string) (string, bool) { return TemplateIdentity(fx.eff, fx.templates, ref) })
	tracker.ObserveConditions(
		func(scope map[string]any) string {
			if v, _ := scope["is_coordinator"].(bool); v {
				return RecipientCoordinator
			}
			return RecipientWorker
		},
		func(gc *oar.GuardContext) string { return gc.Session.Role },
	)
	restore := tracker.Install()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Catalog: fx.eff})
	for _, vars := range renders {
		if _, err := engine.Render(t.Context(), "guidance/ctl-root.md", vars); err != nil {
			restore()
			t.Fatalf("render: %v", err)
		}
	}
	pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{fx.rule}), nil, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	for _, occ := range occurrences {
		gc := oar.NewGuardContext()
		gc.Session.Role = RecipientCoordinator
		gc.Session.SessionMode = occ.mode
		gc.Invocation.Tool = occ.tool
		if _, err := pipeline.EvaluateBlock(t.Context(), oar.AnchorToolPreInvoke, gc); err != nil {
			restore()
			t.Fatalf("evaluate rule: %v", err)
		}
	}
	restore()
	var sites []ConditionSite
	for _, s := range fx.sites {
		if s.ID == site {
			sites = append(sites, s)
		}
	}
	if len(sites) != 1 {
		t.Fatalf("site %s not discovered once in %d sites", site, len(fx.sites))
	}
	var observed []hoststate.ObservedCondition
	for _, o := range tracker.Conditions() {
		if o.Site == site {
			observed = append(observed, o)
		}
	}
	l, err := BuildConditionLedger(sites, observed, constraints)
	contractcheck.FailErr(t, "build ledger", err)
	return l
}
