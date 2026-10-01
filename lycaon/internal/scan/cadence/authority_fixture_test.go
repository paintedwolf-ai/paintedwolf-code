package cadence

import (
	"strings"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func authorityTestStore(t *testing.T) *scanbase.SQLStore {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	return scanbase.NewSQLStore(database)
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

func completeNextScan(t *testing.T, store *scanbase.SQLStore, findings ...api.SecurityFinding) api.CodeScan {
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
