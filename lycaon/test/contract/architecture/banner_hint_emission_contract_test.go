package contract

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/guidance"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/guidancescan"
)

// TestBannerHintCodesHaveProductionReference rejects unreachable banner codes.
func TestBannerHintCodesHaveProductionReference(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)

	scan, err := guidancescan.ScanGuidanceEmission(lycaonRoot)
	contractcheck.FailErr(t, "scan guidance emission", err)

	var missing []string
	for code, entry := range cfg.HintCodes {
		if strings.TrimSpace(entry.Emit) != guidance.EmitBanner {
			continue
		}
		if scan.HasEmissionSite(code) {
			continue
		}
		missing = append(missing, code)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("banner hint codes have no production emission site: %v", missing)
	}
}

// TestHintCodeYAMLEmitFieldIsRecognized validates the emit vocabulary.
func TestHintCodeYAMLEmitFieldIsRecognized(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)

	anchors, err := anchorcatalog.Load(filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml"))
	contractcheck.FailErr(t, "load anchor catalog", err)

	allowedPrefixes := []string{"guard:", "rule:", guidance.EmitUIPrefix}
	allowedExact := map[string]bool{
		"":                  true,
		guidance.EmitBanner: true,
	}
	var bad []string
	for code, entry := range cfg.HintCodes {
		emit := strings.TrimSpace(entry.Emit)
		if allowedExact[emit] {
			continue
		}
		if anchorID, ok := guidance.InjectAnchor(emit); ok {
			if !anchors.IDs[anchorID] {
				bad = append(bad, code+":"+emit+" (no anchor "+anchorID+")")
			}
			continue
		}
		ok := false
		for _, p := range allowedPrefixes {
			if strings.HasPrefix(emit, p) {
				ok = true
				break
			}
		}
		if ok {
			continue
		}
		bad = append(bad, code+":"+emit)
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("unrecognized emit values (extend allowed list if intentional): %v", bad)
	}
}
