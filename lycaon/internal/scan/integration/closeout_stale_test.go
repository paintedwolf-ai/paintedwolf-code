package integration

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSecurityCloseoutPendingObligationIsPure(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	projectDir := t.TempDir()
	store, obligation := commitRequiredLanding(t, sqlDB, projectDir, "dep-pending")
	checker := &scan.SecurityCloseoutChecker{Store: store, Evidence: inspector.NewJSONLStore(inspector.DefaultEvidenceDir)}

	err := checker.Check(t.Context(), "dep-pending", projectDir)
	pending, ok := scan.AsGatePending(err)
	if !ok || pending.ScanID != obligation.ID {
		t.Fatalf("pending = %#v err=%v", pending, err)
	}
	scans, err := store.ListByCanonicalPath(t.Context(), projectDir)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(scans) != 1 {
		t.Fatalf("closeout predicate created work: scans=%d", len(scans))
	}
}

func TestSecurityCloseoutFreshAnchoredObligationPasses(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	projectDir := t.TempDir()
	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	store, obligation := commitRequiredLanding(t, sqlDB, projectDir, "dep-pass")
	completeSecurityEvidence(t, store, evidenceStore, projectDir, obligation, &scanoutput.Result{})

	checker := &scan.SecurityCloseoutChecker{Store: store, Evidence: evidenceStore}
	if err := checker.Check(t.Context(), "dep-pass", projectDir); err != nil {
		t.Fatalf("expected pass: %v", err)
	}
}

func TestSecurityCloseoutFailedEvidenceBlocks(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	projectDir := t.TempDir()
	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	store, obligation := commitRequiredLanding(t, sqlDB, projectDir, "dep-fail")
	completeSecurityEvidence(t, store, evidenceStore, projectDir, obligation, &scanoutput.Result{
		FindingsCount: 1,
		Findings: []api.SecurityFinding{
			scanfindings.FixtureFinding("lycaon.ruby.sql-string-concat", api.FindingLevelHigh, "", "main.go", 1),
		},
	})

	err := (&scan.SecurityCloseoutChecker{Store: store, Evidence: evidenceStore}).Check(t.Context(), "dep-fail", projectDir)
	if err == nil {
		t.Fatal("expected security gate failure")
	}
	if _, ok := scan.AsGatePending(err); ok {
		t.Fatalf("failed evidence must block, not wait: %v", err)
	}
}

func TestSecurityCloseoutMainOffBypassesObligation(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	projectDir := t.TempDir()
	store, _ := commitRequiredLanding(t, sqlDB, projectDir, "dep-off")
	settingsStore, err := settings.NewSecurityScannersStoreAt(filepath.Join(t.TempDir(), "security.yaml"))
	testutil.FailErr(t, "NewSecurityScannersStoreAt", err)
	testutil.FailErr(t, "PutGlobal", settingsStore.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: boolPtr(false)}))

	checker := &scan.SecurityCloseoutChecker{Store: store, Settings: settingsStore}
	if err := checker.Check(t.Context(), "dep-off", projectDir); err != nil {
		t.Fatalf("main switch should bypass closeout: %v", err)
	}
}
