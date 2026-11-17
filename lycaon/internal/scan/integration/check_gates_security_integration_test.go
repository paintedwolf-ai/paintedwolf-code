//go:build integration

package integration

import (
	"testing"

	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
)

func TestSecurityCloseoutCheckerWaitsForLandedScanThenPasses(t *testing.T) {
	projectDir := t.TempDir()
	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)

	sqlDB := testdbfixture.Open(t, "store.db")
	store, obligation := commitRequiredLanding(t, sqlDB, projectDir, "dep-close")
	checker := &scan.SecurityCloseoutChecker{Store: store, Evidence: evidenceStore}

	err := checker.CheckSession(t.Context(), "session-1", projectDir)
	if pending, ok := scan.AsGatePending(err); !ok || pending.ScanID != obligation.ID {
		t.Fatalf("expected obligation pending, got %v", err)
	}
	completeSecurityEvidence(t, store, evidenceStore, projectDir, obligation, &scanoutput.Result{})
	if err := checker.CheckSession(t.Context(), "session-1", projectDir); err != nil {
		t.Fatalf("expected anchored obligation to pass: %v", err)
	}
}
