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
	// Worker completion summaries store grounding under WorkerSummary.
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

func TestProjectMessageDeduplicatesCloseoutLedgerSnapshotsAndEnrichesCitedRows(t *testing.T) {
	ts := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	gr := &api.CitationGrounding{
		Traced: true,
		EvidenceRecords: []api.CitationGroundingEvidenceRecord{
			{
				Handle:   "read#1",
				Kind:     "read",
				Shape:    "file_region",
				Fidelity: "structured",
				Tool:     "read",
				Path:     "./src/a.go",
				Line:     10,
				Excerpt:  "func main()",
			},
			{
				Handle:   "grep#1",
				Kind:     "grep",
				Shape:    "file_region",
				Fidelity: "structured",
				Tool:     "grep",
				Path:     "./src/b.go",
				Line:     20,
				Excerpt:  "func helper()",
			},
		},
		CitedEvidence: []api.CitationGroundingCitedEvidence{{
			Handle:  "read#1",
			Path:    "./src/a.go",
			Line:    10,
			Excerpt: "func main()",
			Verdict: api.CitationVerdictMatched,
		}},
	}
	rowsTurn1 := ProjectMessage("proj-1", "sess-1", api.Message{
		ID:        "msg-turn-1",
		Role:      api.MessageRoleAssistant,
		CreatedAt: ts,
		Grounding: gr,
	})
	// read#1 is in both EvidenceRecords and CitedEvidence, so it projects once (merged into cited).
	// grep#1 is uncited in this message, so it projects as a session-keyed evidence_record row.
	if len(rowsTurn1) != 2 {
		t.Fatalf("rowsTurn1 = %d, want 2", len(rowsTurn1))
	}
	var uncitedTurn1, citedTurn1 IndexRow
	for _, r := range rowsTurn1 {
		if r.Handle == "grep#1" {
			uncitedTurn1 = r
		} else if r.Handle == "read#1" {
			citedTurn1 = r
		}
	}
	if citedTurn1.Tool != "read" || citedTurn1.Kind != "read" || citedTurn1.Shape != "file_region" || citedTurn1.Path != "src/a.go" {
		t.Fatalf("cited row missing merged record metadata: %+v", citedTurn1)
	}
	if uncitedTurn1.Tool != "grep" || uncitedTurn1.Path != "src/b.go" {
		t.Fatalf("uncited row missing metadata: %+v", uncitedTurn1)
	}

	// A later turn carrying the same uncited ledger snapshot produces the same row ID for grep#1.
	rowsTurn2 := ProjectMessage("proj-1", "sess-1", api.Message{
		ID:        "msg-turn-2",
		Role:      api.MessageRoleAssistant,
		CreatedAt: ts.Add(time.Minute),
		Grounding: &api.CitationGrounding{
			Traced:          true,
			EvidenceRecords: gr.EvidenceRecords[1:],
		},
	})
	if len(rowsTurn2) != 1 || rowsTurn2[0].ID != uncitedTurn1.ID {
		t.Fatalf("rowsTurn2 = %+v, want session-stable ID %q", rowsTurn2, uncitedTurn1.ID)
	}
}
