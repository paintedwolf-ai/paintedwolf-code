package promptrender_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

// Every atom of every compound stock guard either flips the guard's actual
// decision between two observed evaluations that hold the other atoms fixed,
// or is ruled out by the guard's logic or a recorded production constraint.
// Observations come from the host-state matrix, the fuzz target's seeds and
// committed corpus, and cases generated for each gap. The ledger is logged as
// the receipt.
func TestGuardAtomsHaveIndependentWitnesses(t *testing.T) {
	stock, err := promptrender.LoadStock(t.Context())
	if err != nil {
		t.Fatalf("load stock catalog: %v", err)
	}
	ledger, err := promptrender.StockConditionLedger(t.Context(), stock, fuzzCorpusConditions(t, stock))
	if err != nil {
		t.Fatalf("build condition ledger: %v", err)
	}
	raw, err := json.Marshal(ledger)
	if err != nil {
		t.Fatalf("encode condition ledger: %v", err)
	}
	t.Logf("condition ledger: sites=%d compound=%d obligations=%d witnesses=%d exclusions=%d gaps=%d stale=%d generated_targets=%d generated_found=%d generated_runs=%d generated_seconds=%.0f",
		len(ledger.Sites), ledger.CompoundSites, ledger.Obligations, len(ledger.Witnesses), len(ledger.Exclusions), len(ledger.Gaps), len(ledger.StaleConstraints),
		ledger.Generation.Targets, ledger.Generation.Found, ledger.Generation.Runs, ledger.Generation.Seconds)
	t.Logf("condition ledger JSON:\n%s", raw)
	sum, err := promptrender.RunCartesianMatrix(t.Context(), stock)
	if err != nil {
		t.Fatalf("host-state matrix: %v", err)
	}
	cov := sum.Coverage
	t.Logf("render coverage: registered=%d rendered=%d unrendered=%d expected_unrendered=%d rules=%d template_branches=%d rule_branches=%d observed_families=%v observed_recipients=%v",
		len(cov.DiscoveredTemplates), cov.RenderedTemplates, len(cov.UnrenderedTemplates), len(cov.ExpectedUnrendered),
		len(cov.DiscoveredRules), len(cov.TemplateBranches), len(cov.RuleBranches), ledger.ObservedFamilies, ledger.ObservedRecipients)
	t.Logf("unrendered templates: %v", cov.UnrenderedTemplates)
	if err := ledger.RequireObserved([]string{"template", "oar"}, []string{interactions.RecipientCoordinator, interactions.RecipientWorker}); err != nil {
		t.Error(err)
	}
	if err := ledger.ValidateZeroUnexplainedGaps(); err != nil {
		contractcheck.FailErr(t, "ledger.ValidateZeroUnexplainedGaps failed", err)
	}
}

// fuzzCorpusConditions replays the fuzz target's seeds and committed corpus
// through its case executor with the ledger's attribution, so the guard
// evaluations the corpus reaches join the ledger.
func fuzzCorpusConditions(t *testing.T, stock *promptrender.Stock) []hoststate.ObservedCondition {
	t.Helper()
	env, err := loadFuzzEnvOnce()
	if err != nil {
		t.Fatalf("loadFuzzEnvOnce: %v", err)
	}
	seeds := fuzzSeeds(t, env.domains)
	names := make([]string, len(seeds))
	for i := range seeds {
		names[i] = "fuzz seed " + strconv.Itoa(i)
	}
	files, err := filepath.Glob(filepath.Join("testdata", "fuzz", "FuzzRecipientGuidanceAcrossHostStates", "*"))
	contractcheck.FailErr(t, "filepath.Glob failed", err)
	for _, f := range files {
		seeds = append(seeds, readCorpusEntry(t, f))
		names = append(names, "fuzz corpus "+filepath.Base(f))
	}
	tracker, err := promptrender.NewConditionTracker(stock)
	if err != nil {
		t.Fatalf("condition tracker: %v", err)
	}
	restore := tracker.Install()
	defer restore()
	for i, seed := range seeds {
		tracker.SetCase(names[i])
		if _, err := executeFuzzRecipientCase(t.Context(), env, seed, t.TempDir()); err != nil {
			t.Fatalf("%s: %v", names[i], err)
		}
	}
	return tracker.Conditions()
}

// readCorpusEntry reads the single []byte value of a Go fuzz corpus file.
func readCorpusEntry(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	header, value, ok := strings.Cut(strings.TrimSpace(string(raw)), "\n")
	inner, found := strings.CutPrefix(strings.TrimSpace(value), "[]byte(")
	if !ok || header != "go test fuzz v1" || !found || !strings.HasSuffix(inner, ")") {
		t.Fatalf("%s: not a one-value []byte fuzz corpus entry", path)
	}
	data, err := strconv.Unquote(strings.TrimSuffix(inner, ")"))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return []byte(data)
}
