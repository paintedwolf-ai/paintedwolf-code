package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/guidancescan"
)

func TestSynthesisGuardCodesHaveEmissionSite(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	registered := make(map[string]bool, len(cfg.HintCodes))
	for code := range cfg.HintCodes {
		registered[code] = true
	}
	scan, err := guidancescan.ScanGuidanceEmission(lycaonRoot)
	contractcheck.FailErr(t, "scan guidance emission sites", err)
	for _, code := range evidenceGroundedSynthesisCodes {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Fatalf("missing hint %q", code)
		}
		if entry.Emit != "guard:coordinator_synthesis" {
			t.Fatalf("hint %q emit = %q want guard:coordinator_synthesis", code, entry.Emit)
		}
		if !scan.HasEmissionSite(code) {
			t.Fatalf("hint %q must be referenced from production code (Format path or literal ref)", code)
		}
	}
}

func TestSynthesisGroundingRetryPolicy(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load citation repair policy", err)
	for _, code := range evidenceGroundedSynthesisCodes {
		wantRetry := code != guidance.SynthCitationsRequiredCode
		if got := cfg.IsInSessionRetry(code); got != wantRetry {
			t.Fatalf("synthesis code %q retry = %v, want %v", code, got, wantRetry)
		}
	}
}

func TestEvidenceGroundedWorkerSummaryCodesHaveEmitChannel(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	for _, code := range evidenceGroundedWorkerSummaryCodes {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Fatalf("missing hint %q", code)
		}
		if entry.Emit != "guard:worker_summary" {
			t.Fatalf("hint %q emit = %q want guard:worker_summary", code, entry.Emit)
		}
	}
}
