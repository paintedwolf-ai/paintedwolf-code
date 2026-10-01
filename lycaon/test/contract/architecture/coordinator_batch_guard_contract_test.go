package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/guidancescan"
)

var coordinatorBatchGuardCodes = []string{
	guard.CoordinatorBatchAlreadyClosedCode,
	guard.CoordinatorBatchWrongPhaseCode,
}

func TestCoordinatorBatchGuardCodesHaveEmissionSite(t *testing.T) {
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
	for _, code := range coordinatorBatchGuardCodes {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Fatalf("missing hint %q", code)
		}
		if entry.Emit != "guard:coordinator" {
			t.Fatalf("hint %q emit = %q want guard:coordinator", code, entry.Emit)
		}
		if !scan.HasEmissionSite(code) {
			t.Fatalf("hint %q must be referenced from production code", code)
		}
	}
}
