package execution

import (
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

// A path scan only fixes findings in the paths it named.
func TestDiffAgainstOpenScopesFixesToScannedPaths(t *testing.T) {
	held := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
	elsewhere := scanfindings.FixtureFinding("rule-b", api.FindingLevelHigh, "b", "b.go", 1)
	open := map[string]api.SecurityFinding{
		scanbase.FindingIdentity(held):      held,
		scanbase.FindingIdentity(elsewhere): elsewhere,
	}

	introduced, fixed := diffAgainstOpen(open, nil, []string{"a.go"})
	if len(introduced) != 0 {
		t.Fatalf("introduced = %d, want 0", len(introduced))
	}
	if len(fixed) != 1 || fixed[0].RuleID != "rule-a" {
		t.Fatalf("fixed = %+v, want only the finding under the scanned path", fixed)
	}

	_, fullFixed := diffAgainstOpen(open, nil, nil)
	if len(fullFixed) != 2 {
		t.Fatalf("a full pass fixed %d, want both", len(fullFixed))
	}
}
