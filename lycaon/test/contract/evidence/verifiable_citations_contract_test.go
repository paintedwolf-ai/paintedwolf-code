package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

// verifiableVerdicts is the locked three-tier verdict enum.
// Add tiers only with an SSOT + lock update.
var verifiableVerdicts = []string{
	"matched",
	"traced",
	"unverifiable",
}

func TestVerdictWireEnumLocked(t *testing.T) {
	t.Parallel()
	got := wirespec.GoEnumValues(
		evidence.VerdictMatched,
		evidence.VerdictTraced,
		evidence.VerdictUnverifiable,
	)
	contractcheck.FailSetEqual(t, "evidence.Verdict wire vs SSOT", got, verifiableVerdicts)
}

func TestCoordinatorReportUsesTripleCitedEvidence(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "guidance", "coordinator_completion_report.go")
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read coordinator_completion_report.go", err)
	text := string(data)
	if strings.Contains(text, "CitedEvidence  []string") {
		t.Fatal("coordinator report must not use handle-keyed []string cited_evidence")
	}
	if !strings.Contains(text, "CoordinatorCitedEvidence") {
		t.Fatal("coordinator report must use CoordinatorCitedEvidence triples")
	}
}
