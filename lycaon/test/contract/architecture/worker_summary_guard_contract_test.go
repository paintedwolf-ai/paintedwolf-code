package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/guidancescan"
)

func TestWorkerSummaryGuardCodesHaveEmissionSite(t *testing.T) {
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
	codes := append(append([]string{}, workerSummaryGuardCodesPreEvidenceGrounding...), evidenceGroundedWorkerSummaryCodes...)
	for _, code := range codes {
		if _, ok := cfg.HintCodes[code]; !ok {
			t.Fatalf("missing hint %q", code)
		}
		if !scan.HasEmissionSite(code) {
			t.Fatalf("hint %q must be referenced from production code (Format path or literal ref)", code)
		}
	}
}
