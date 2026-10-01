package search_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectGateEvidenceMapsVerdict(t *testing.T) {
	rec := evidence.GateRecord(
		evidence.GateTypeSurveyClaims,
		"claims",
		"run_1",
		evidence.GateVerdictApproved,
		"CLAIMED",
		map[string]any{
			"threat_model": "public HTTPS API; unauthenticated clients",
			"claims":       "1. SQLi in a.go:1",
			"cited_evidence": []any{
				map[string]any{"path": "a.go", "line": 1, "excerpt": "q"},
			},
		},
		"", "", "", 0, time.Date(2026, 7, 8, 15, 4, 5, 0, time.UTC),
	)
	rows := search.ProjectGateEvidence(search.ProjectGateEvidenceInput{
		ProjectID:     "proj",
		SessionID:     "sess",
		WorkflowRunID: "run_1",
		Record:        rec,
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %d want 1", len(rows))
	}
	row := rows[0]
	if row.HitKind != search.HitKindOutcome || row.Source != search.SourceGateEvidence {
		t.Fatalf("hit/source = %s/%s", row.HitKind, row.Source)
	}
	if row.Kind != string(evidence.GateTypeSurveyClaims) {
		t.Fatalf("kind = %q", row.Kind)
	}
	if row.WorkflowRunID != "run_1" || row.Verdict != string(evidence.GateVerdictApproved) {
		t.Fatalf("run/verdict = %q/%q", row.WorkflowRunID, row.Verdict)
	}
	if !strings.Contains(row.Snippet, "claims: 1. SQLi") {
		t.Fatalf("snippet = %q", row.Snippet)
	}
	again := search.ProjectGateEvidence(search.ProjectGateEvidenceInput{
		ProjectID: "proj", SessionID: "sess", WorkflowRunID: "run_1", Record: rec,
	})
	if again[0].ID != row.ID {
		t.Fatal("row id must be deterministic")
	}
}

func TestRunAndVerdictDSLFilters(t *testing.T) {
	plan, err := search.CompileQuery(`run:run_1 verdict:approved`, search.CompileContext{})
	testutil.FailErr(t, "CompileQuery", err)
	if plan.Store == nil {
		t.Fatal("want store leg")
	}
	sql := plan.Store.SQL
	if !strings.Contains(sql, "workflow_run_id") || !strings.Contains(sql, "verdict") {
		t.Fatalf("SQL missing run/verdict predicates: %s", sql)
	}
}

func TestProjectGateEvidenceWriteThrough(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	ctx := context.Background()
	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-1", testdbseed.DefaultProjectID, t.TempDir())

	rec := evidence.GateRecord(
		evidence.GateTypeOptionsJudge,
		"judge",
		"run_gate",
		evidence.GateVerdictApproved,
		"SELECTED",
		map[string]any{"winner": "B", "cited_evidence": []any{map[string]any{"path": "p", "line": 1, "excerpt": "e"}}},
		"", "", "", 0, time.Now().UTC(),
	)
	testutil.FailErr(t, "write-through", search.ProjectGateEvidenceComplete(ctx, sqlDB, search.ProjectGateEvidenceInput{
		ProjectID:     testdbseed.DefaultProjectID,
		SessionID:     "sess-1",
		WorkflowRunID: "run_gate",
		Record:        rec,
	}))

	var count int
	testutil.FailErr(t, "count", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM evidence_index
		WHERE workflow_run_id = ? AND verdict = ? AND hit_kind = ?
	`, "run_gate", string(evidence.GateVerdictApproved), search.HitKindOutcome).Scan(&count))
	if count != 1 {
		t.Fatalf("write-through count = %d want 1", count)
	}
}
