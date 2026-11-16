package search_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectScanFindingsKeysOnFingerprint(t *testing.T) {
	f := api.SecurityFinding{
		RuleID:  "osv:CVE-2024-1",
		Level:   api.FindingLevelHigh,
		Message: "test vuln",
		Locations: []api.SecurityFindingLocation{{
			URI:       "go.mod",
			StartLine: 1,
		}},
		Fingerprints: api.SecurityFindingFingerprints{Primary: "fp-deadbeef1234"},
		Tool:         api.ToolDescriptor{DriverID: "trivy", Name: "Trivy"},
		Properties: &api.SecurityFindingProperties{
			Lycaon: &api.SecurityFindingLycaonProperties{Kind: api.FindingKindSCA},
		},
	}
	rows := search.ProjectScanFindings("proj", "scan-1", []api.SecurityFinding{f}, "2026-01-01T00:00:00Z")
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Handle != f.Fingerprints.Primary {
		t.Fatalf("handle = %q want %q", rows[0].Handle, f.Fingerprints.Primary)
	}
}

// A finished scan's findings are indexed under the project its scope
// belongs to; a scan whose scope names nothing known indexes nothing.
func TestFinishedScanIndexesFindingsUnderItsScopesProject(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "scan-project.db")
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, sqlDB, "sess-scan-proj", testdbseed.DefaultProjectID)

	_, err := sqlDB.ExecContext(ctx, `INSERT INTO workers (
		id, project_id, session_id, name, agent_type, scope_mode, delegation_id,
		operation_id, created_at, updated_at
	) VALUES ('w-del-1', ?, 'sess-scan-proj', 'impl-1', 'implementer', 'read', 'del-legacy-1', 'op-w1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert worker with delegation_id", err)

	ingestBytes, err := json.Marshal(map[string]any{
		"findings": []api.SecurityFinding{{
			RuleID:       "rule-1",
			Level:        api.FindingLevelMedium,
			Message:      "issue",
			Locations:    []api.SecurityFindingLocation{{URI: "main.go", StartLine: 3}},
			Fingerprints: api.SecurityFindingFingerprints{Primary: "fp-session-1"},
			Tool:         api.ToolDescriptor{DriverID: "semgrep", Name: "Semgrep"},
		}},
	})
	testutil.FailErr(t, "marshal ingest findings", err)

	for _, tc := range []struct {
		scanID  string
		scopeID string
		project string
	}{
		{scanID: "scan-by-session", scopeID: "sess-scan-proj", project: testdbseed.DefaultProjectID},
		{scanID: "scan-by-delegation", scopeID: "del-legacy-1", project: testdbseed.DefaultProjectID},
		{scanID: "scan-unmatched", scopeID: "unknown-scope"},
	} {
		_, err = sqlDB.ExecContext(ctx, `INSERT INTO code_scans (
			id, canonical_path, categories_json, status, delegation_id, reuse_key,
			ingest_json, created_at, completed_at
		) VALUES (?, '', '["sast"]', 'complete', ?, ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:01Z')`,
			tc.scanID, tc.scopeID, "rk-"+tc.scanID, string(ingestBytes))
		testutil.FailErr(t, "insert code_scan", err)
		testutil.FailErr(t, "ProjectScanComplete", search.ProjectScanComplete(ctx, sqlDB, tc.scanID))

		rows, err := sqlDB.QueryContext(ctx, `SELECT project_id, handle FROM evidence_index WHERE source_ref = ?`, tc.scanID)
		testutil.FailErr(t, "read index", err)
		var indexed [][2]string
		for rows.Next() {
			var row [2]string
			testutil.FailErr(t, "scan index row", rows.Scan(&row[0], &row[1]))
			indexed = append(indexed, row)
		}
		testutil.FailErr(t, "index rows", rows.Close())
		var want [][2]string
		if tc.project != "" {
			want = [][2]string{{tc.project, "fp-session-1"}}
		}
		if !slices.Equal(indexed, want) {
			t.Fatalf("scan %s indexed %v, want %v", tc.scanID, indexed, want)
		}
	}
}
