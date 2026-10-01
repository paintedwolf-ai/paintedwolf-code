package scan

import (
	"errors"
	"strings"
	"testing"
	"time"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func authorityTestStore(t *testing.T) *SQLStore {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	return NewSQLStore(database)
}

func authorityScan(id, assessmentID, root, snapshot, scanner string, target api.ScanTargetKind, deleted []string) api.CodeScan {
	return api.CodeScan{
		ID: id, AssessmentID: assessmentID, CanonicalPath: root,
		SourceSnapshotID: snapshot, ScannerID: scanner,
		Categories: []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
		Status:     api.CodeScanStatusPending, Trigger: api.ScanTriggerManual,
		TargetKind: target, DeletedPaths: append([]string(nil), deleted...),
		ExecutionManifest: &api.ScanExecutionManifest{
			SchemaVersion: "v1", ScannerID: scanner, Engine: "test", Driver: "test",
			ScopeKind: string(scancatalog.ScopeSourceDriver), DefinitionFingerprint: strings.Repeat("a", 64),
			FingerprintScheme: api.ScanFingerprintScheme,
		},
		ExecutionFingerprint: strings.Repeat("b", 64),
		FingerprintScheme:    api.ScanFingerprintScheme,
		SourceCaptureQuality: "exact", SourceAdmissionMode: string(sourcesnapshot.AdmissionScope),
		CreatedAt: time.Now().UTC(),
	}
}

func completeNextScan(t *testing.T, store *SQLStore, findings ...api.SecurityFinding) api.CodeScan {
	t.Helper()
	claimed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "ClaimNext", err)
	won, err := store.MarkComplete(t.Context(), claimed, &scanoutput.Result{FindingsCount: len(findings), Findings: findings})
	testutil.FailErr(t, "MarkComplete", err)
	if !won {
		t.Fatalf("scan %s lost completion claim", claimed.ID)
	}
	completed, err := store.Get(t.Context(), claimed.ID)
	testutil.FailErr(t, "Get completed", err)
	return *completed
}

func TestIncrementalFindingSetCarriesUntouchedAndAppliesDeletion(t *testing.T) {
	store := authorityTestStore(t)
	root := t.TempDir()
	baseline := authorityScan("scan-base", "assessment-base", root, "snapshot-1", "sast", api.ScanTargetFull, nil)
	testutil.FailErr(t, "insert baseline", store.Insert(t.Context(), baseline, nil, ""))
	a1 := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
	b1 := scanfindings.FixtureFinding("rule-b", api.FindingLevelMedium, "b", "b.go", 1)
	completeNextScan(t, store, a1, b1)

	changed := authorityScan("scan-change", "assessment-change", root, "snapshot-2", "sast", api.ScanTargetPaths, nil)
	testutil.FailErr(t, "insert incremental", store.Insert(t.Context(), changed, []string{"a.go"}, "snapshot-1"))
	a2 := scanfindings.FixtureFinding("rule-a-new", api.FindingLevelCritical, "a2", "a.go", 2)
	afterChange := completeNextScan(t, store, a2)
	if afterChange.CoverageStatus != api.ScanCoverageComplete || len(afterChange.Findings) != 2 {
		t.Fatalf("incremental finding set = coverage %q findings %#v", afterChange.CoverageStatus, afterChange.Findings)
	}
	if scanfindings.PrimaryURI(afterChange.Findings[0]) == scanfindings.PrimaryURI(afterChange.Findings[1]) {
		t.Fatalf("untouched b.go finding was not carried forward: %#v", afterChange.Findings)
	}

	deleted := authorityScan("scan-delete", "assessment-delete", root, "snapshot-3", "sast", api.ScanTargetPaths, []string{"b.go"})
	testutil.FailErr(t, "insert deletion", store.Insert(t.Context(), deleted, nil, "snapshot-2"))
	afterDelete := completeNextScan(t, store)
	if afterDelete.CoverageStatus != api.ScanCoverageComplete || len(afterDelete.Findings) != 1 || scanfindings.PrimaryURI(afterDelete.Findings[0]) != "a.go" {
		t.Fatalf("deletion reduction = coverage %q findings %#v", afterDelete.CoverageStatus, afterDelete.Findings)
	}
}

func TestIncrementalFindingSetWithoutCompatibleBaseIsPartial(t *testing.T) {
	store := authorityTestStore(t)
	scan := authorityScan("scan-partial", "assessment-partial", t.TempDir(), "snapshot-1", "sast", api.ScanTargetPaths, nil)
	testutil.FailErr(t, "insert path scan", store.Insert(t.Context(), scan, []string{"a.go"}, ""))
	completed := completeNextScan(t, store)
	if completed.CoverageStatus != api.ScanCoveragePartial || completed.FindingSetID == "" {
		t.Fatalf("coverage = %q finding_set_id=%q", completed.CoverageStatus, completed.FindingSetID)
	}
}

func TestBoundedAdmissionReportsBoundedCoverageAndStillBasesIncrements(t *testing.T) {
	store := authorityTestStore(t)
	root := t.TempDir()
	scan := authorityScan("scan-bounded", "assessment-bounded", root, "snapshot-1", "sast", api.ScanTargetFull, nil)
	scan.SourceAdmissionMode = string(sourcesnapshot.AdmissionScopeBounded)
	testutil.FailErr(t, "insert bounded scan", store.Insert(t.Context(), scan, nil, ""))
	a1 := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a1", "a.go", 1)
	b1 := scanfindings.FixtureFinding("rule-b", api.FindingLevelHigh, "b1", "b.go", 1)
	completed := completeNextScan(t, store, a1, b1)
	if completed.CoverageStatus != api.ScanCoverageBounded {
		t.Fatalf("bounded admission coverage = %q, want bounded: the engine read every admitted file", completed.CoverageStatus)
	}

	changed := authorityScan("scan-bounded-delta", "assessment-bounded-delta", root, "snapshot-2", "sast", api.ScanTargetPaths, nil)
	changed.SourceAdmissionMode = string(sourcesnapshot.AdmissionScopeBounded)
	testutil.FailErr(t, "insert bounded delta", store.Insert(t.Context(), changed, []string{"a.go"}, "snapshot-1"))
	a2 := scanfindings.FixtureFinding("rule-a-new", api.FindingLevelCritical, "a2", "a.go", 2)
	afterChange := completeNextScan(t, store, a2)
	if afterChange.CoverageStatus != api.ScanCoverageBounded || len(afterChange.Findings) != 2 {
		t.Fatalf("bounded delta = coverage %q findings %#v; the bounded run must serve as its base", afterChange.CoverageStatus, afterChange.Findings)
	}
}

func TestAssessmentViewKeepsCurrentWhenLatestAttemptFails(t *testing.T) {
	store := authorityTestStore(t)
	root := t.TempDir()
	insertAssessment := func(id, snapshot string) {
		t.Helper()
		_, err := store.EnsureAssessment(t.Context(), AssessmentDraft{
			ID: id, CanonicalPath: root, SourceSnapshotID: snapshot,
			RequiredScanners: []string{"sast", "secrets"},
			Target:           TargetSelection{Kind: api.ScanTargetFull}, Trigger: api.ScanTriggerManual,
		})
		testutil.FailErr(t, "ensureAssessment", err)
		for _, scannerID := range []string{"sast", "secrets"} {
			scan := authorityScan(id+"-"+scannerID, id, root, snapshot, scannerID, api.ScanTargetFull, nil)
			testutil.FailErr(t, "insert assessment member", store.Insert(t.Context(), scan, nil, ""))
		}
	}

	insertAssessment("assessment-good", "snapshot-1")
	completeNextScan(t, store)
	completeNextScan(t, store)
	time.Sleep(time.Millisecond)
	insertAssessment("assessment-bad", "snapshot-2")
	failed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim failed member", err)
	won, err := store.FinalizeFailure(t.Context(), failed, api.CodeScanStatusFailed, "SCAN_ENGINE_FAILED", "engine failed")
	testutil.FailErr(t, "FinalizeFailure", err)
	if !won {
		t.Fatal("failed member lost claim")
	}
	completeNextScan(t, store)

	view, err := store.AssessmentView(t.Context(), []string{root})
	testutil.FailErr(t, "AssessmentView", err)
	if view.CurrentID != "assessment-good" || view.LatestAttemptID != "assessment-bad" {
		t.Fatalf("assessment view = %+v", view)
	}
	latest := BuildAssessmentSummary(view.LatestAttemptID, view.LatestAttempt)
	if latest == nil || latest.Status != api.CodeScanStatusFailed || latest.CoverageStatus != api.ScanCoverageUnavailable {
		t.Fatalf("latest attempt summary = %+v", latest)
	}
}

func TestCompareRejectsIncompatibleAuthorities(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name      string
		mutateOld func(*api.CodeScan)
		mutateNew func(*api.CodeScan)
		wantCode  string
	}{
		{
			name: "scanner", wantCode: CompareRejectScannerMismatch,
			mutateNew: func(scan *api.CodeScan) {
				scan.ScannerID = "secrets"
				scan.ExecutionManifest.ScannerID = "secrets"
			},
		},
		{
			name: "fingerprint scheme", wantCode: CompareRejectFingerprintScheme,
			mutateNew: func(scan *api.CodeScan) {
				scan.FingerprintScheme = "other-scheme"
				scan.ExecutionManifest.FingerprintScheme = "other-scheme"
			},
		},
		{
			name: "definition", wantCode: CompareRejectDefinitionMismatch,
			mutateNew: func(scan *api.CodeScan) {
				scan.ExecutionFingerprint = strings.Repeat("c", 64)
			},
		},
		{
			name: "coverage", wantCode: CompareRejectCoverageIncomplete,
			mutateNew: func(scan *api.CodeScan) {
				scan.SourceCaptureQuality = "observed"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := authorityTestStore(t)
			oldScan := authorityScan("old", "assessment-old", root, "snapshot-old", "sast", api.ScanTargetFull, nil)
			newScan := authorityScan("new", "assessment-new", root, "snapshot-new", "sast", api.ScanTargetFull, nil)
			if tc.mutateOld != nil {
				tc.mutateOld(&oldScan)
			}
			if tc.mutateNew != nil {
				tc.mutateNew(&newScan)
			}
			testutil.FailErr(t, "insert old", store.Insert(t.Context(), oldScan, nil, ""))
			completeNextScan(t, store)
			testutil.FailErr(t, "insert new", store.Insert(t.Context(), newScan, nil, ""))
			completeNextScan(t, store)

			_, err := (&CoordinatorImpl{Store: store}).Compare(t.Context(), oldScan.ID, newScan.ID)
			var reject *CompareReject
			if !errors.As(err, &reject) || reject.Code != tc.wantCode {
				t.Fatalf("compare error = %v, want %s", err, tc.wantCode)
			}
		})
	}
}
