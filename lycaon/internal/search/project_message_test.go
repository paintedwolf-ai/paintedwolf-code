package search

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectMessageGroundingRows(t *testing.T) {
	ts := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	msg := api.Message{
		ID:        "msg-1",
		Role:      api.MessageRoleAssistant,
		Content:   "summary",
		CreatedAt: ts,
		Grounding: &api.CitationGrounding{
			Traced:   true,
			HintCode: "HINT",
			CitedEvidence: []api.CitationGroundingCitedEvidence{{
				Handle:  "read#1",
				Path:    "src/a.go",
				Line:    10,
				Excerpt: "func main()",
				Verdict: api.CitationVerdictMatched,
			}},
		},
	}
	rows := ProjectMessage("proj-1", "sess-1", msg)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.ProjectID != "proj-1" || row.SessionID != "sess-1" || row.MessageID != "msg-1" {
		t.Fatalf("identity = %+v", row)
	}
	if row.Source != SourceMessage || row.HitKind != HitKindEvidence || row.Handle != "read#1" {
		t.Fatalf("projection = %+v", row)
	}
	if row.Verified == nil || !*row.Verified {
		t.Fatalf("verified = %+v", row.Verified)
	}
}

func TestProjectMessageWorkerSummaryGrounding(t *testing.T) {
	ts := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	// Worker completion summaries carry no top-level Content/Grounding; the
	// grounding lives under WorkerSummary and must still be surfaced.
	msg := api.Message{
		ID:        "msg-ws",
		Role:      api.MessageRoleAssistant,
		CreatedAt: ts,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "job-1",
			Grounding: &api.CitationGrounding{
				Traced: true,
				Findings: []api.CitationGroundingFinding{{
					Handle:  "finding#1",
					Path:    "src/b.go",
					Line:    7,
					Note:    "worker found a race",
					Verdict: api.CitationVerdictMatched,
				}},
			},
		},
	}
	rows := ProjectMessage("proj-1", "sess-1", msg)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.MessageID != "msg-ws" || row.Handle != "finding#1" || row.HitKind != HitKindClaim {
		t.Fatalf("worker summary projection = %+v", row)
	}
}

func TestProjectToolMessageWebSearch(t *testing.T) {
	ts := time.Now().UTC()
	content := `{"query":"cowrie","results":[{"title":"Cowrie","url":"https://example.com/cowrie","snippet":"honeypot"}]}`
	msg := api.Message{
		ID:        "tool-1",
		Role:      api.MessageRoleTool,
		Content:   content,
		CreatedAt: ts,
		ToolResult: &api.ToolResult{
			Content: content,
			Outcome: api.ToolResultOutcomeCompleted,
		},
	}
	rows := ProjectToolMessage("proj-1", "sess-1", msg, "web_search")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].HitKind != HitKindWeb || rows[0].URL != "https://example.com/cowrie" {
		t.Fatalf("web row = %+v", rows[0])
	}
}

func TestRowIDDeterministic(t *testing.T) {
	a := RowID(SourceMessage, "m1", "suffix")
	b := RowID(SourceMessage, "m1", "suffix")
	if a != b {
		t.Fatalf("RowID not deterministic: %q vs %q", a, b)
	}
	if c := RowID(SourceMessage, "m1", "other"); c == a {
		t.Fatal("RowID collision across suffix")
	}
}
