package cost

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectReportPagesGlobalSortAndLiteralSearch(t *testing.T) {
	tracker, database := newTestTracker(t, nil)
	testdbseed.InsertProject(t, database, "p")
	_, err := database.ExecContext(t.Context(), `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<125)
 INSERT INTO sessions(id,project_id,owner_person_id,title,posture,status,created_at,activity_at,updated_at,archived_at)
 SELECT printf('s-%03d',i),'p',(SELECT id FROM people WHERE role = 'owner'),printf('Chat %03d',i),'build','idle','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',
 CASE WHEN i%2=0 THEN '2026-01-02T00:00:00Z' END FROM n`)
	testutil.FailErr(t, "seed chats", err)
	for i := 1; i <= 125; i++ {
		value := int64(i) * 1_000_000
		testutil.FailErr(t, "record chat usage", tracker.RecordUsage(t.Context(), UsageEvent{ProjectID: "p", SessionID: fmt.Sprintf("s-%03d", i), Caller: CallerCoordinator, PromptTokens: 126 - i, EstimatedNanoUSD: &value}))
	}
	seen := make(map[string]bool)
	query := ReportQuery{Limit: 17, Sort: "cost"}
	for {
		page, err := tracker.ProjectReport(t.Context(), "p", query)
		testutil.FailErr(t, "read cost page", err)
		if page.Total != 125 || page.SessionCount != 125 || page.ArchivedSessionCount != 62 || page.MaxSessionTokens != 125 || page.MaxSessionNanoUsd != 125_000_000 {
			t.Fatalf("metadata changed between pages: %+v", page)
		}
		if len(page.Sessions) > 17 {
			t.Fatalf("unbounded page: %d", len(page.Sessions))
		}
		for _, row := range page.Sessions {
			if seen[row.Session.ID] {
				t.Fatalf("duplicate session %s", row.Session.ID)
			}
			want := fmt.Sprintf("s-%03d", 125-len(seen))
			if row.Session.ID != want {
				t.Fatalf("cost order: got %s want %s", row.Session.ID, want)
			}
			seen[row.Session.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		query.Cursor = page.NextCursor
	}
	if len(seen) != 125 {
		t.Fatalf("visited %d chats", len(seen))
	}
	tokens, err := tracker.ProjectReport(t.Context(), "p", ReportQuery{Limit: 1, Sort: "tokens"})
	testutil.FailErr(t, "sort by tokens", err)
	if tokens.Sessions[0].Session.ID != "s-001" {
		t.Fatalf("token order: %+v", tokens.Sessions)
	}
	filtered, err := tracker.ProjectReport(t.Context(), "p", ReportQuery{Search: "%"})
	testutil.FailErr(t, "literal search", err)
	if filtered.Total != 0 || len(filtered.Sessions) != 0 || filtered.SessionCount != 125 || filtered.Summary.EstimatedNanoUsd != tokens.Summary.EstimatedNanoUsd {
		t.Fatalf("search changed global totals or treated wildcard as syntax: %+v", filtered)
	}
	_, err = tracker.ProjectReport(t.Context(), "p", ReportQuery{Cursor: tokens.NextCursor, Sort: "cost"})
	if !errors.Is(err, pagecursor.ErrInvalid) {
		t.Fatalf("cross-sort cursor: %v", err)
	}
}

func TestCostReadsCompactTotalsAcrossReceiptCorrections(t *testing.T) {
	tracker, database := newTestTracker(t, nil)
	testdbseed.InsertSession(t, database, "s", "p")
	_, err := database.ExecContext(t.Context(), `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<10000)
 INSERT INTO llm_calls(id,project_id,session_id,caller,status,started_at,prompt_tokens,completion_tokens,estimated_nano_usd,rate_snapshot)
 SELECT printf('call-%05d',i),'p','s','coordinator','reported','2026-01-01T00:00:00Z',10,2,1000,'[]' FROM n`)
	testutil.FailErr(t, "seed large receipt history", err)
	var count int
	testutil.FailErr(t, "count compact totals", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM llm_cost_totals`).Scan(&count))
	if count != 1 {
		t.Fatalf("%d totals for identical attribution", count)
	}
	summary, err := tracker.Summary(t.Context(), api.CostScopeSession, "s", "")
	testutil.FailErr(t, "summary without receipt payload decoding", err)
	if summary.TokenTotals.Prompt != 100000 || summary.TokenTotals.Completion != 20000 || summary.EstimatedNanoUsd != 10_000_000 {
		t.Fatalf("summary = %+v", summary)
	}
	_, err = database.ExecContext(t.Context(), `UPDATE llm_calls SET status='unknown', prompt_tokens=0, completion_tokens=0, estimated_nano_usd=NULL WHERE id='call-00001'`)
	testutil.FailErr(t, "correct receipt to unknown", err)
	_, err = database.ExecContext(t.Context(), `DELETE FROM llm_calls WHERE id='call-00002'`)
	testutil.FailErr(t, "remove receipt", err)
	after, err := tracker.ProjectReport(t.Context(), "p", ReportQuery{})
	testutil.FailErr(t, "read corrected report", err)
	if after.Summary.TokenTotals.Prompt != 99980 || after.Summary.UnknownChargedCalls != 1 || after.Sessions[0].Cost.UnknownCalls != 1 {
		t.Fatalf("corrections not reflected: %+v", after)
	}
}

func TestCostTotalsPricingTimestampOrdersFractionalSeconds(t *testing.T) {
	tracker, _ := newTestTracker(t, nil)
	whole := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fraction := whole.Add(100 * time.Millisecond)
	for _, at := range []time.Time{fraction, whole} {
		amount := int64(10_000_000)
		testutil.FailErr(t, "record priced usage", tracker.RecordUsage(t.Context(), UsageEvent{ProjectID: "p", SessionID: "s", Caller: CallerCoordinator, EstimatedNanoUSD: &amount, PricingSource: "fixture", PricedAsOf: at}))
	}
	summary, err := tracker.Summary(t.Context(), api.CostScopeProject, "", "p")
	testutil.FailErr(t, "read pricing provenance", err)
	if len(summary.PricingProvenance) != 1 || summary.PricingProvenance[0].PricedAt == nil || !summary.PricingProvenance[0].PricedAt.Equal(fraction) {
		t.Fatalf("newest pricing timestamp = %+v", summary.PricingProvenance)
	}
}
