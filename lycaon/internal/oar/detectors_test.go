package oar

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSecretMatchDetectorRegistered(t *testing.T) {
	reg := NewDetectorRegistry()
	reg.Register(SecretMatchDetector{Matcher: secretmatch.NewInertMatcher()})
	gc := NewGuardContext()
	gc.Content.Content = "no secrets here"
	findings, err := reg.Dispatch("detector://secretmatch", gc)
	testutil.FailErr(t, "dispatch secretmatch", err)
	if len(findings) != 1 || findings[0].Fact != "secret_matches" {
		t.Fatalf("got %#v", findings)
	}
}

func TestDetectorApplyFindingsAndWhen(t *testing.T) {
	l, err := NewLoader("")
	testutil.FailErr(t, "loader", err)
	rule := &Rule{
		OAR:      SupportedSpecVersion,
		ID:       "DETECTOR_ECHO_THRESHOLD",
		Kind:     KindDetector,
		Anchor:   AnchorToolPreInvoke,
		Effect:   EffectBlock,
		Emit:     "guard:detector",
		When:     "prompt_injection_score > 0.8",
		Detector: &DetectorRef{Ref: "detector://echo"},
		OnError:  "fail_closed",
	}
	reg := NewDetectorRegistry()
	reg.Register(EchoDetector{Score: 0.91})
	rule = loadDetectorTestRule(t, l, reg, rule, "prompt-injection")
	p := NewGuardPipeline(NewRuleSet([]*Rule{rule}), l, NewCounterStore())
	p.SetDetectors(reg)

	res, err := p.Evaluate(context.Background(), StagePreInvoke, NewGuardContext())
	testutil.FailErr(t, "evaluate", err)
	if res.Decision == nil || res.Decision.Effect != EffectBlock {
		t.Fatalf("expected block, got %#v", res.Decision)
	}

	reg2 := NewDetectorRegistry()
	reg2.Register(EchoDetector{Score: 0})
	p.SetDetectors(reg2)
	res, err = p.Evaluate(context.Background(), StagePreInvoke, NewGuardContext())
	testutil.FailErr(t, "evaluate zero score", err)
	if res.Decision != nil {
		t.Fatalf("score 0 should not fire threshold, got %#v", res.Decision)
	}
}

func TestDetectorOnErrorFailClosed(t *testing.T) {
	l, err := NewLoader("")
	testutil.FailErr(t, "loader", err)
	rule := &Rule{
		OAR:      SupportedSpecVersion,
		ID:       "DETECTOR_FAIL",
		Kind:     KindDetector,
		Anchor:   AnchorToolPreInvoke,
		Effect:   EffectBlock,
		Emit:     "guard:detector",
		When:     "prompt_injection_score > 0.8",
		Detector: &DetectorRef{Ref: "detector://always-fail"},
		OnError:  "fail_closed",
	}
	reg := NewDetectorRegistry()
	reg.Register(ErrDetectorAlwaysFail{})
	rule = loadDetectorTestRule(t, l, reg, rule, "prompt-injection")
	p := NewGuardPipeline(NewRuleSet([]*Rule{rule}), l, NewCounterStore())
	p.SetDetectors(reg)
	res, err := p.Evaluate(context.Background(), StagePreInvoke, NewGuardContext())
	testutil.FailErr(t, "evaluate", err)
	if res.Decision == nil || res.Decision.Effect != EffectBlock {
		t.Fatalf("fail_closed should treat detector error as block, got %#v", res.Decision)
	}
}

// [OAR-DOC-9] Provenance never permits replacing the same qualified rule.
func TestMergeTieredRejectsEveryDuplicateIdentity(t *testing.T) {
	for _, tier := range []Tier{TierBuiltin, TierPack} {
		first := NewRuleSet([]*Rule{{ID: "R1", Kind: KindPolicy, Anchor: "a", Effect: EffectBlock, Tier: TierBuiltin, Source: "builtin", Mandatory: true}})
		second := NewRuleSet([]*Rule{{ID: "R1", Kind: KindPolicy, Anchor: "a", Effect: EffectWarn, Tier: tier, Source: "extension"}})
		if _, err := MergeTiered(first, second); err == nil {
			t.Fatalf("duplicate identity accepted from %s", tier)
		}
	}
}

func TestDoc9LoadTiersRejectsPackReplacingBuiltin(t *testing.T) {
	ensureCatalog(t)
	l, err := NewLoader("")
	testutil.FailErr(t, "loader", err)

	writeHint := func(dir, code, effect string) {
		t.Helper()
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o700))
		body := "oar: '1.0'\nid: " + code + "\nkind: policy\nanchor: tool.pre_invoke\neffect: " + effect + "\nwhen: 'true'\ncopy:\n  what: test\n"

		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, code+".yaml"), []byte(body), 0o600))
	}

	builtin := t.TempDir()
	pack := t.TempDir()
	writeHint(builtin, "DEMO_RULE", "block")
	writeHint(pack, "DEMO_RULE", "warn")

	_, err = l.LoadTiers(TierLoad{BuiltinDir: extpacks.OnDisk(builtin), Packs: []PackSource{{Name: "fixture", Dir: pack}}})
	if err == nil || !strings.Contains(err.Error(), "[OAR-DOC-9]") {
		t.Fatalf("load duplicate identity: %v", err)
	}
}

// [OAR-EVAL-13] References resolve against the complete rule set, across packs.
func TestEval13LoadTiersLinksCrossPackOverrides(t *testing.T) {
	l := testLoader(t)
	builtin, pack := t.TempDir(), t.TempDir()
	base := "oar: '1.0'\nnamespace: example.base\nid: BASE\nkind: policy\nanchor: tool.pre_invoke\neffect: block\n"
	exception := "oar: '1.0'\nnamespace: example.extension\nid: EXCEPTION\nkind: policy\nanchor: tool.pre_invoke\neffect: allow\noverrides: [example.base/BASE]\n"
	testutil.FailErr(t, "write base", os.WriteFile(filepath.Join(builtin, "arbitrary.yaml"), []byte(base), 0o600))
	testutil.FailErr(t, "write exception", os.WriteFile(filepath.Join(pack, "another-name.yaml"), []byte(exception), 0o600))
	rules, err := l.LoadTiers(TierLoad{BuiltinDir: extpacks.OnDisk(builtin), Packs: []PackSource{{Name: "extension", Dir: pack}}})
	testutil.FailErr(t, "link cross-pack override", err)
	pipeline := NewGuardPipeline(rules, l, NewCounterStore())
	result, err := pipeline.Evaluate(context.Background(), StagePreInvoke, NewGuardContext())
	testutil.FailErr(t, "evaluate linked rules", err)
	if result.Decision != nil || len(result.Trace.Entries) != 2 || result.Trace.Entries[1].Rule != "example.base/BASE" || result.Trace.Entries[1].Outcome != TraceSuppressed {
		t.Fatalf("cross-pack exception did not suppress base: decision=%#v trace=%#v", result.Decision, result.Trace)
	}
}

// EchoDetector returns a fixed score for conformance fixtures (echo of fixture input).
type EchoDetector struct {
	Score float64
	Fact  string // default prompt_injection_score
}

func (EchoDetector) Name() string { return "echo" }

func (e EchoDetector) Inspect(_ *GuardContext) ([]Finding, error) {
	fact := e.Fact
	if fact == "" {
		fact = "prompt_injection_score"
	}
	return []Finding{{Fact: fact, Score: e.Score}}, nil
}

// ErrDetectorAlwaysFail is a test double that always errors (on_error paths).
type ErrDetectorAlwaysFail struct {
	Err error
}

func (ErrDetectorAlwaysFail) Name() string { return "always-fail" }

func (d ErrDetectorAlwaysFail) Inspect(_ *GuardContext) ([]Finding, error) {
	if d.Err != nil {
		return nil, d.Err
	}
	return nil, fmt.Errorf("detector always_fail")
}

// Detector tests load the capability and document through the production path.
func loadDetectorTestRule(t *testing.T, loader *Loader, registry *DetectorRegistry, rule *Rule, profile string) *Rule {
	t.Helper()
	cap := *InstalledCapabilityDocument()
	cap.Profiles = append(append([]string{}, cap.Profiles...), profile)
	cap.Detectors = append(append([]string{}, cap.Detectors...), rule.Detector.Ref)
	loader.SetCapability(&cap)
	loader.SetDetectors(registry)
	anchor := rule.Anchor
	for core, local := range cap.Anchors.Core {
		if local == anchor {
			anchor = core
			break
		}
	}
	loaded, _, err := loader.parseRule(rule.ID, map[string]any{
		"oar": "1.0", "id": rule.ID, "kind": "detector", "anchor": anchor,
		"effect": string(rule.Effect), "when": rule.When, "detector": map[string]any{"ref": rule.Detector.Ref},
		"requires": map[string]any{"profiles": []any{profile}},
	})
	testutil.FailErr(t, "load detector document", err)
	return loaded
}
