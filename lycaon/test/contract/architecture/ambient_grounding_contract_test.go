package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/guidancescan"
)

var ambientGroundingCodes = evidenceGroundedAmbientCodes

func TestAmbientGroundingCodesHaveEmissionSite(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	scan, err := guidancescan.ScanGuidanceEmission(lycaonRoot)
	contractcheck.FailErr(t, "scan guidance emission sites", err)
	for _, code := range ambientGroundingCodes {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Fatalf("missing hint %q", code)
		}
		if entry.Emit != "guard:ambient_grounding" {
			t.Fatalf("hint %q emit = %q want guard:ambient_grounding", code, entry.Emit)
		}
		if !scan.HasEmissionSite(code) {
			t.Fatalf("hint %q must be referenced from production code", code)
		}
	}
}
