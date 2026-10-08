package oar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/testutil"
)

func schemaDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(testutil.CheckoutRoot(t), "schemas")
}

// hintsDir is the shipped platform policy directory. It is bundled config, not
// a host path — the tests below load the same tree the binary ships.
func hintsDir(t *testing.T) extpacks.Source {
	t.Helper()
	return extpacks.Bundled(hintregistry.DefaultDir)
}

func loadStockRules(t *testing.T) *RuleSet {
	t.Helper()
	ensureCatalog(t)
	l, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "loader", err)
	rs, err := l.LoadEffectivePolicy()
	testutil.FailErr(t, "load stock", err)
	return rs
}

func TestCounterStoreIncrementReset(t *testing.T) {
	s := NewCounterStore()
	if s.Get("sess", "DOOM_LOOP_REPEAT", CounterFire) != 0 {
		t.Fatal("expected 0")
	}
	if got := s.Increment("sess", "DOOM_LOOP_REPEAT", CounterFire, 1); got != 1 {
		t.Fatalf("got %d", got)
	}
	s.Increment("sess", "DOOM_LOOP_REPEAT", CounterFire, 1)
	if s.Get("sess", "DOOM_LOOP_REPEAT", CounterFire) != 2 {
		t.Fatal("expected 2")
	}
	s.Reset("sess", "DOOM_LOOP_REPEAT", CounterFire)
	if s.Get("sess", "DOOM_LOOP_REPEAT", CounterFire) != 0 {
		t.Fatal("expected reset 0")
	}
}

func TestGuardContextLazyProvider(t *testing.T) {
	gc := NewGuardContext()
	calls := 0
	gc.RegisterProvider("paintedwolf.workers_idle", func(g *GuardContext) error {
		calls++
		g.WorkersIdle = true
		return nil
	})
	testutil.FailErr(t, "ensure", gc.Ensure("paintedwolf.workers_idle"))
	testutil.FailErr(t, "ensure again", gc.Ensure("paintedwolf.workers_idle"))
	if calls != 1 {
		t.Fatalf("provider called %d times", calls)
	}
	if !gc.WorkersIdle {
		t.Fatal("expected paintedwolf.workers_idle")
	}
}

func TestValidateConditionRejectsUnknownFact(t *testing.T) {
	err := checkWhenAgainstSpec("workers_idel == true", nil)
	if err == nil {
		t.Fatal("expected error for unknown fact")
	}
}

func TestEvaluateConditionGroundingFacts(t *testing.T) {
	gc := NewGuardContext()
	gc.ClaimsCompletion = true
	gc.HasMatchingLedgerJob = false
	gc.BreakerCount = 2
	when := `paintedwolf.claims_completion && !paintedwolf.has_matching_ledger_job && breaker_count < 3`
	ok, err := EvaluateCondition(when, gc)
	testutil.FailErr(t, "eval", err)
	if !ok {
		t.Fatal("expected fire")
	}
	gc.BreakerCount = 3
	ok, err = EvaluateCondition(when, gc)
	testutil.FailErr(t, "eval2", err)
	if ok {
		t.Fatal("expected not fire at breaker_count >= 3")
	}
}

func TestPathOutsideScopeFunction(t *testing.T) {
	gc := NewGuardContext()
	gc.PathOutsideScopeByTool = map[string]bool{"read": true}
	ok, err := EvaluateCondition(`path_outside_scope("read")`, gc)
	testutil.FailErr(t, "eval", err)
	if !ok {
		t.Fatal("expected path_outside_scope true")
	}
}

func TestFlowMatches(t *testing.T) {
	if !FlowMatches([]string{"read", "edit", "command"}, []string{"read", "command"}) {
		t.Fatal("expected subsequence match")
	}
	if FlowMatches([]string{"read", "edit"}, []string{"command"}) {
		t.Fatal("expected no match")
	}
}

func TestLoadCorpusValidates(t *testing.T) {
	rs := loadStockRules(t)
	if rs.Len() < 200 {
		t.Fatalf("expected many OAR rules, got %d", rs.Len())
	}
	for _, code := range []string{
		"SPEC_POSTURE_STATE_FORBIDDEN",
		"SPEC_POSTURE_DELEGATION_FORBIDDEN",
		"DISALLOWED_AGENT",
	} {
		r, ok := rs.Get(code)
		if !ok {
			t.Fatalf("missing %s", code)
		}
		if strings.TrimSpace(r.When) == "" {
			t.Fatalf("%s: expected condition", code)
		}
	}
}

func TestEvaluateBlockObservedPostureRefusal(t *testing.T) {
	rs := loadStockRules(t)
	l, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "loader", err)
	p := NewGuardPipeline(rs, l, NewCounterStore())
	p.EnableAnchor(AnchorToolRejected)

	gc := NewGuardContext()
	gc.Tool = "state_start"
	gc.SessionPosture = "spec"
	gc.ObservedRejectCode = "SPEC_POSTURE_STATE_FORBIDDEN"
	res, err := p.EvaluateBlock(t.Context(), AnchorToolRejected, gc)
	testutil.FailErr(t, "eval", err)
	if res.Decision == nil || res.Decision.Code != "SPEC_POSTURE_STATE_FORBIDDEN" {
		t.Fatalf("decision = %#v", res.Decision)
	}
	if res.Decision.Effect != EffectBlock {
		t.Fatalf("effect %s", res.Decision.Effect)
	}
}

func TestIntegrityWhenCompile(t *testing.T) {
	ensureCatalog(t)
	l, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "loader", err)
	rs, err := l.LoadDir(hintsDir(t))
	testutil.FailErr(t, "load", err)
	cfg, err := guidance.LoadHintConfig(hintsDir(t))
	testutil.FailErr(t, "hints", err)
	reg := filepath.Join(t.TempDir(), "guidance_registry.json")
	// Temp registry so the check does not write the repo file.
	testutil.FailErr(t, "sync registry", SyncRegistryFromHintConfig(cfg, reg))
	rep, err := CheckIntegrity(hintsDir(t), reg, rs, cfg)
	testutil.FailErr(t, "integrity", err)
	if rep.ConditionsValidated < 7 {
		t.Fatalf("expected >=7 validated conditions, got %d", rep.ConditionsValidated)
	}
	if len(rep.RegistryMissing) > 0 {
		t.Fatalf("registry missing %d codes (e.g. %v)", len(rep.RegistryMissing), rep.RegistryMissing[:min(5, len(rep.RegistryMissing))])
	}
	if len(rep.RegistryOrphans) > 0 {
		t.Fatalf("registry orphans %v", rep.RegistryOrphans)
	}
}

func TestSyncRegistryThreadsReferences(t *testing.T) {
	dir := t.TempDir()
	body := `hint_codes:
  DEMO_OWASP_TAG:
    oar: "1.0"
    id: DEMO_OWASP_TAG
    kind: policy
    anchor: tool.pre_invoke
    effect: block
    when: "true"
    selector: {}
    copy:
      what: demo
    references:
      owasp_llm: [LLM01]
      mitre_atlas: [AML.T0051]
    x-paintedwolf-emit: guard:test
    x-paintedwolf-category: recoverable
`
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "DEMO_OWASP_TAG.yaml"), []byte(body), 0o600))
	regPath := filepath.Join(t.TempDir(), "guidance_registry.json")
	cfg, err := guidance.LoadHintConfig(extpacks.OnDisk(dir))
	testutil.FailErr(t, "load hints", err)
	testutil.FailErr(t, "sync", SyncRegistryFromHintConfig(cfg, regPath))
	raw, err := os.ReadFile(regPath)
	testutil.FailErr(t, "read", err)
	var doc struct {
		HintCodes map[string]map[string]any `json:"hint_codes"`
	}
	testutil.FailErr(t, "json", json.Unmarshal(raw, &doc))
	row := doc.HintCodes["DEMO_OWASP_TAG"]
	refs, ok := row["references"].(map[string]any)
	if !ok {
		t.Fatalf("missing references: %#v", row)
	}
	owasp, _ := refs["owasp_llm"].([]any)
	if len(owasp) != 1 || owasp[0] != "LLM01" {
		t.Fatalf("owasp_llm=%#v", refs["owasp_llm"])
	}
}

func TestSyncRegistryFromStockIncludesNonPlatformPack(t *testing.T) {
	reg := filepath.Join(t.TempDir(), "guidance_registry.json")
	testutil.FailErr(t, "sync stock", SyncRegistryFromStock(reg))
	raw, err := os.ReadFile(reg)
	testutil.FailErr(t, "read", err)
	var doc struct {
		HintCodes map[string]map[string]any `json:"hint_codes"`
	}
	testutil.FailErr(t, "json", json.Unmarshal(raw, &doc))
	// Non-platform packs must land; a platform-only DefaultDir sync drops these.
	for _, code := range []string{
		"ASK_USER_ALREADY_PENDING",      // hitl
		"BANNER_PROMOTE_HIGH_CONFLICT",  // implement
		"BROWSER_UNAVAILABLE",           // browser
		"SECURITY_DISABLED",             // scan-guidance
		"POLICY_WRITE_REQUIRES_COMMAND", // platform
	} {
		if _, ok := doc.HintCodes[code]; !ok {
			t.Fatalf("stock sync missing %s (got %d codes)", code, len(doc.HintCodes))
		}
	}
	if n := len(doc.HintCodes); n < 300 {
		t.Fatalf("stock sync too small: %d codes (expected full pack union)", n)
	}
}

func TestKernelRefusalSilenceIsComposedByThePublishedRule(t *testing.T) {
	rule, ok := loadStockRules(t).Get("SANDBOX_REFUSAL_REPORT_SILENT")
	if !ok {
		t.Fatal("kernel refusal silence rule is missing")
	}
	for _, applied := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			for _, witness := range []string{"", "kernel", "incomplete", "unavailable"} {
				for _, report := range []bool{false, true} {
					gc := NewGuardContext()
					gc.ConfineApplied = applied
					gc.SandboxRefusalWitness = witness
					if failed {
						gc.FailedStages = []string{"tool"}
					}
					if report {
						gc.SandboxRefusals = []string{"file-read-data: /fixture"}
					}
					fires, err := EvaluateCondition(rule.When, gc)
					testutil.FailErr(t, "evaluate published kernel-refusal warning", err)
					want := applied && failed && witness == "kernel" && !report
					if fires != want {
						t.Fatalf("applied=%v failed=%v witness=%q report=%v fires=%v, want %v", applied, failed, witness, report, fires, want)
					}
				}
			}
		}
	}
}
