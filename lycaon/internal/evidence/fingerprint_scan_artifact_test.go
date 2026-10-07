package evidence_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

func seedCompletedScanWithFindings(t *testing.T, store *scan.SQLStore, findings []api.SecurityFinding) string {
	t.Helper()
	coord := scantest.Coordinator(t, store, nil)
	created, err := coord.Enqueue(context.Background(), scan.EnqueueRequest{
		ProjectDir: t.TempDir(),
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
	})
	testutil.FailErr(t, "coord.Enqueue failed", err)
	claimed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	if claimed.ID != created.ID {
		t.Fatalf("claimed scan %q want %q", claimed.ID, created.ID)
	}
	if _, err := store.MarkComplete(context.Background(), claimed, &scanoutput.Result{
		FindingsCount: len(findings),
		Findings:      findings,
	}); err != nil {
		testutil.FailErr(t, "store.MarkComplete failed", err)
	}
	rec := evidence.Record{
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"findings": findings,
			"guidance": []api.ScanGuidanceSummary{},
		},
	}
	if err := store.SaveIngest(context.Background(), created.ID, rec); err != nil {
		testutil.FailErr(t, "store.SaveIngest failed", err)
	}
	return created.ID
}

// realScanQueryWireJSON runs coord.Query and marshals the response — the same JSON
// scan_query returns on the wire (SecurityFinding locations[].uri, not guidance "file").
func realScanQueryWireJSON(t *testing.T) (projectDir, scanID, flaggedPath, message, body string) {
	t.Helper()
	dir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "scan-evidence.db")

	store := scan.NewSQLStore(sqlDB)
	coord := scantest.Coordinator(t, store, nil)
	flaggedPath = "internal/auth/handler.go"
	message = "SQL injection via string concatenation"
	scanID = seedCompletedScanWithFindings(t, store, []api.SecurityFinding{
		scanfindings.FixtureFinding("lycaon.ruby.sql-string-concat", api.FindingLevelHigh, message, flaggedPath, 42),
	})

	resp, err := coord.Query(context.Background(), scan.QueryRequest{ScanID: scanID})
	testutil.FailErr(t, "coord.Query failed", err)
	raw, err := surveyjson.Marshal(resp)
	testutil.FailErr(t, "marshal ScanQueryResponse failed", err)
	return dir, scanID, flaggedPath, message, string(raw)
}

func TestScanQueryRealWireJSONGroundsFinding(t *testing.T) {
	dir, scanID, flaggedPath, message, body := realScanQueryWireJSON(t)

	rec := evidence.BuildEvidenceRecord(dir, "scan_query", map[string]any{"scan_id": scanID}, body)
	if rec.Kind != "scan" || rec.Shape != evidence.ShapeArtifact {
		t.Fatalf("rec = %+v want scan/artifact", rec)
	}
	if rec.Fidelity != evidence.FidelityStructured {
		t.Fatalf("trust = %q want structured", rec.Fidelity)
	}
	if len(rec.Body) == 0 {
		t.Fatal("scan_query record must capture body for artifact verifier")
	}
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Excerpt: message}); !ok {
		t.Fatal("finding message must ground against captured scan_query body")
	}
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Path: flaggedPath}); !ok {
		t.Fatalf("finding uri %q must index in ByPath", flaggedPath)
	}
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Excerpt: "fabricated excerpt not in scan output"}); ok {
		t.Fatal("fabricated excerpt must not ground")
	}
}

func TestScanQueryLedgerHandleGroundsTypedCitation(t *testing.T) {
	_, scanID, flaggedPath, message, body := realScanQueryWireJSON(t)

	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "scan_query", ID: "c1", Args: map[string]any{"scan_id": scanID}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: body,
		}},
	})
	rec, ok := evidence.ResolveHandle(ev, "scan#1")
	if !ok {
		t.Fatalf("handles = %v", evidence.HandlesSorted(ev))
	}
	if rec.Kind != "scan" || rec.Shape != evidence.ShapeArtifact {
		t.Fatalf("rec = %+v", rec)
	}
	if _, ok := evidence.HandleForPath(ev, flaggedPath); !ok {
		t.Fatalf("path %q not in ledger ByPath: %v", flaggedPath, evidence.ObservedPathsSorted(ev))
	}
	res := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path:    flaggedPath,
		Line:    42,
		Excerpt: message,
	}, ev, "")
	if res.Verdict != evidence.VerdictMatched {
		t.Fatalf("verdict = %q want matched (handle=%q)", res.Verdict, res.Handle)
	}
}

func TestScanPackRealWireJSONGroundsPerScanPreview(t *testing.T) {
	dir := t.TempDir()
	flagged := "pkg/auth/token.go"
	message := "Hardcoded credential in source"
	payload, err := surveyjson.Marshal(scantoolapi.ScanPackToolResult{
		ScanIDs: []string{"engine-1"},
		Status:  api.CodeScanStatusComplete,
		PerScan: []scantoolapi.ScanPackPerScan{{
			ScanID:    "engine-1",
			ScannerID: "lycaon-secrets",
			Status:    api.CodeScanStatusComplete,
			Guidance: []api.ScanGuidanceSummary{{
				File:    flagged,
				Message: message,
				RuleID:  "gitleaks:github-pat",
				Code:    "SCAN_HARDCODED_SECRET",
			}},
		}},
		FindingsCount: 1,
	})
	testutil.FailErr(t, "marshal ScanPackToolResult failed", err)

	rec := evidence.BuildEvidenceRecord(dir, "scan_pack", map[string]any{}, string(payload))
	if len(rec.Body) == 0 {
		t.Fatal("scan_pack record must capture body")
	}
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Excerpt: message}); !ok {
		t.Fatal("guidance_preview message must ground")
	}
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Path: flagged}); !ok {
		t.Fatalf("preview file %q must index", flagged)
	}
}

func TestScanSummaryRealWireJSONIndexesGuidanceFile(t *testing.T) {
	dir := t.TempDir()
	flagged := "app/models/user.rb"
	summary, err := json.Marshal(api.CodeScan{
		ID:     "scan-1",
		Status: api.CodeScanStatusComplete,
		Guidance: []api.ScanGuidanceSummary{{
			File:    flagged,
			Message: "Unsafe deserialization",
			RuleID:  "brakeman:deserialize",
		}},
		FindingsByLevel: map[string]int{"high": 1},
	})
	testutil.FailErr(t, "marshal CodeScan failed", err)

	rec := evidence.BuildEvidenceRecord(dir, "scan_summary", map[string]any{"scan_id": "scan-1"}, string(summary))
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Path: flagged}); !ok {
		t.Fatalf("summary guidance file %q must index", flagged)
	}
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Excerpt: "Unsafe deserialization"}); !ok {
		t.Fatal("summary guidance message must ground")
	}
}

func TestScanListRealWireJSONCapturesEmptyHint(t *testing.T) {
	dir := t.TempDir()
	body, err := surveyjson.Marshal(scantoolapi.NewListScansResponse(t.Context(), nil))
	testutil.FailErr(t, "marshal ListScansResponse failed", err)

	rec := evidence.BuildEvidenceRecord(dir, "scan_list", nil, string(body))
	if len(rec.Body) == 0 {
		t.Fatal("scan_list must capture body for SCAN_LIST_EMPTY hint grounding")
	}
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Excerpt: "SCAN_LIST_EMPTY"}); !ok {
		// Hint text is bundled — verify hint_code at minimum appears in body.
		if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Excerpt: `"hint_code":"SCAN_LIST_EMPTY"`}); !ok {
			t.Fatal("empty list hint_code must appear in captured body")
		}
	}
}

func TestScanCompareWireJSONIndexesResolvedPaths(t *testing.T) {
	dir := t.TempDir()
	body, err := surveyjson.Marshal(scan.Comparison{
		OldScanID: "old",
		NewScanID: "new",
		ResolvedFindings: []api.SecurityFinding{
			scanfindings.FixtureFinding("gitleaks:token", api.FindingLevelHigh, "leak", "secrets.env", 2),
		},
		ResolvedCount: 1,
	})
	testutil.FailErr(t, "marshal compare", err)

	rec := evidence.BuildEvidenceRecord(dir, "scan_compare", nil, string(body))
	if rec.Kind != "scan" || rec.Shape != evidence.ShapeArtifact {
		t.Fatalf("rec = %+v", rec)
	}
	if len(rec.Body) == 0 {
		t.Fatal("scan_compare must capture body")
	}
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Path: "secrets.env"}); !ok {
		t.Fatal("resolved finding path must index")
	}
}

func TestScanGroupsViewIndexesLocationPaths(t *testing.T) {
	groupBody := `{
		"scan_id": "scan-1",
		"view": "groups",
		"groups": [
			{
				"id": "group-1",
				"rule_id": "r1",
				"message": "dependency vulnerability",
				"locations": [
					{"uri": "lycaon/go.mod", "start_line": 1}
				]
			}
		]
	}`
	rec := evidence.BuildEvidenceRecord("", "scan_query", map[string]any{"scan_id": "scan-1", "view": "groups"}, groupBody)
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Path: "lycaon/go.mod"}); !ok {
		t.Fatal("group location uri must index in ByPath")
	}
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Excerpt: "dependency vulnerability"}); !ok {
		t.Fatal("group message must ground against captured scan body")
	}
}

