package scan

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanReadRequiresPersistedAuthorityFacts(t *testing.T) {
	store := authorityTestStore(t)
	record := authorityScan("scan", "assessment", t.TempDir(), "snapshot", "sast", api.ScanTargetFull, nil)
	record.Status = api.CodeScanStatusComplete
	record.CoverageStatus = api.ScanCoverageComplete
	testutil.FailErr(t, "insert completed scan", store.Insert(t.Context(), record, nil, ""))
	ingest, err := json.Marshal(IngestArtifacts{CoverageStatus: api.ScanCoverageComplete})
	testutil.FailErr(t, "encode ingest result", err)
	_, err = store.db.ExecContext(t.Context(), "UPDATE code_scans SET ingest_json = ? WHERE id = ?", string(ingest), record.ID)
	testutil.FailErr(t, "store ingest result", err)
	_, err = store.db.ExecContext(t.Context(), "DELETE FROM scan_run_facts WHERE scan_id = ?", record.ID)
	testutil.FailErr(t, "remove required authority facts", err)
	got, err := store.Get(t.Context(), record.ID)
	if got != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read missing authority = %+v, %v; want missing-row error", got, err)
	}
}

func TestIncrementalBaseBreaksTimestampTiesByAdmission(t *testing.T) {
	store := authorityTestStore(t)
	root := t.TempDir()
	for _, id := range []string{"z-older", "a-newer"} {
		record := authorityScan("scan-"+id, "assessment-"+id, root, "snapshot-base", "sast", api.ScanTargetFull, nil)
		record.Status = api.CodeScanStatusComplete
		record.CoverageStatus = api.ScanCoverageComplete
		testutil.FailErr(t, "insert completed base scan", store.Insert(t.Context(), record, nil, ""))
		testutil.FailErr(t, "insert tied finding set", store.queries.InsertScanFindingSet(t.Context(), db.InsertScanFindingSetParams{
			ID: id, ScanID: record.ID, CanonicalPath: root, ScannerID: record.ScannerID,
			SourceSnapshotID: record.SourceSnapshotID, ExecutionFingerprint: record.ExecutionFingerprint,
			FingerprintScheme: record.FingerprintScheme, CoverageStatus: string(record.CoverageStatus),
			WarningsJson: "[]", CreatedAt: db.FormatTime(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)),
		}))
		finding := scanfindings.FixtureFinding(id, api.FindingLevelHigh, id, "untouched.go", 1)
		raw, err := json.Marshal(finding)
		testutil.FailErr(t, "encode base finding", err)
		testutil.FailErr(t, "insert base finding", store.queries.InsertFindingEntry(t.Context(), db.InsertFindingEntryParams{
			FindingSetID: id, FindingJson: string(raw),
		}))
		_, err = store.queries.BindScanFindingSet(t.Context(), db.BindScanFindingSetParams{
			FindingSetID: id, CoverageStatus: string(record.CoverageStatus), ScanID: record.ID,
		})
		testutil.FailErr(t, "bind base finding set", err)
		rollup, err := json.Marshal(rollupFindings([]api.SecurityFinding{finding}))
		testutil.FailErr(t, "encode base rollup", err)
		testutil.FailErr(t, "store base rollup", store.queries.PutFindingRollup(t.Context(), db.PutFindingRollupParams{
			FindingSetID: id, RollupJson: string(rollup),
		}))
		testutil.FailErr(t, "refresh base assessment", refreshAssessmentRollup(t.Context(), store.queries, record.AssessmentID))
	}
	increment := authorityScan("increment", "assessment-increment", root, "snapshot-next", "sast", api.ScanTargetPaths, nil)
	testutil.FailErr(t, "insert incremental scan", store.Insert(t.Context(), increment, []string{"changed.go"}, "snapshot-base"))
	completed := completeNextScan(t, store)
	if len(completed.Findings) != 1 || completed.Findings[0].RuleID != "a-newer" {
		t.Fatalf("increment carried %+v; want newest base finding", completed.Findings)
	}
}

func TestCoverageRequiresDeclaredCaptureAndAdmission(t *testing.T) {
	for _, tc := range []struct {
		quality   string
		admission string
		want      api.ScanCoverageStatus
	}{
		{"", "scope", api.ScanCoveragePartial},
		{"observed", "scope", api.ScanCoveragePartial},
		{"exact", "", api.ScanCoveragePartial},
		{"exact", "unknown", api.ScanCoveragePartial},
		{"exact", "scope", api.ScanCoverageComplete},
		{"exact", "scope_bounded", api.ScanCoverageBounded},
	} {
		t.Run(tc.quality+"/"+tc.admission, func(t *testing.T) {
			if got := scanoutput.CoverageForResult(&scanoutput.Result{}, tc.quality, tc.admission); got != tc.want {
				t.Fatalf("coverage = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestScanReadRequiresReferencedFindingSet(t *testing.T) {
	store := authorityTestStore(t)
	record := authorityScan("scan", "assessment", t.TempDir(), "snapshot", "sast", api.ScanTargetFull, nil)
	testutil.FailErr(t, "insert scan", store.Insert(t.Context(), record, nil, ""))
	completed := completeNextScan(t, store)
	_, err := store.db.ExecContext(t.Context(), "DELETE FROM scan_finding_sets WHERE id = ?", completed.FindingSetID)
	testutil.FailErr(t, "remove referenced finding set", err)
	got, err := store.Get(t.Context(), record.ID)
	if got != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read missing finding set = %+v, %v; want missing-row error", got, err)
	}
}

func TestClaimNextRollsBackMissingAuthorityFacts(t *testing.T) {
	store := authorityTestStore(t)
	record := authorityScan("scan", "assessment", t.TempDir(), "snapshot", "sast", api.ScanTargetFull, nil)
	testutil.FailErr(t, "insert pending scan", store.Insert(t.Context(), record, nil, ""))
	_, err := store.db.ExecContext(t.Context(), "DELETE FROM scan_run_facts WHERE scan_id = ?", record.ID)
	testutil.FailErr(t, "remove required authority facts", err)
	claimed, err := store.ClaimNext(t.Context())
	if claimed != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("claim missing authority = %+v, %v; want missing-row error", claimed, err)
	}
	row, err := store.queries.GetCodeScan(t.Context(), record.ID)
	testutil.FailErr(t, "read scan after rejected claim", err)
	if row.Status != string(api.CodeScanStatusPending) || row.Attempt != 0 ||
		row.ClaimToken.Valid || row.ClaimedBy.Valid || row.HeartbeatAt.Valid || row.LeaseExpiresAt.Valid {
		t.Fatalf("failed claim committed state: %+v", row)
	}
}
