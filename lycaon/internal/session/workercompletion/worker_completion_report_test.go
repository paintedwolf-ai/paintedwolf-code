package workercompletion_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParseWorkerCompletionReportFromJSONFence(t *testing.T) {
	raw := "```json\n{\"leg_status\":\"complete\",\"files_modified\":[\"src/a.go\"],\"objectives_met\":[\"added handler\"],\"remaining_risk\":[],\"suggested_next_task\":\"verify tests\",\"brief\":\"Done.\"}\n```"
	report, ok := workercompletion.ParseWorkerCompletionReport(raw)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if report.LegStatus != "complete" || report.Brief != "Done." {
		t.Fatalf("report = %+v", report)
	}
	if len(report.FilesModified) != 0 {
		t.Fatalf("worker-supplied files_modified must be dropped, got %v", report.FilesModified)
	}
}

func TestParseWorkerCompletionReportFindingPath(t *testing.T) {
	raw := `{"leg_status":"complete","findings":[{"path":"internal/foo/bar.go","line":42,"excerpt":"return nil","note":"nil guard"}]}`
	report, ok := workercompletion.ParseWorkerCompletionReport(raw)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %+v", report.Findings)
	}
	got := report.Findings[0]
	if got.Path != "internal/foo/bar.go" || got.Line != 42 || got.Excerpt != "return nil" || got.Note != "nil guard" {
		t.Fatalf("finding = %+v", got)
	}
}

func TestNormalizeFindingsKeepsClaimAndStripsSeverityUnlessVulnerability(t *testing.T) {
	raw := `{"leg_status":"complete","findings":[
		{"path":"a.go","line":1,"excerpt":"eval","note":"unused CSP","claim":"vulnerability","adversary":"page script","precondition":"packaged webview","severity":"medium"},
		{"path":"b.go","line":2,"excerpt":"0600","note":"documented","claim":"accepted_residual","severity":"high"},
		{"path":"c.go","line":3,"excerpt":"listen","note":"loopback only","claim":"model"}
	]}`
	report, ok := workercompletion.ParseWorkerCompletionReport(raw)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if len(report.Findings) != 3 {
		t.Fatalf("findings = %+v", report.Findings)
	}
	vuln := report.Findings[0]
	if vuln.Claim != workercompletion.FindingClaimVulnerability || vuln.Severity != workercompletion.FindingSeverityMedium || vuln.Adversary == "" {
		t.Fatalf("vulnerability finding = %+v", vuln)
	}
	if report.Findings[1].Claim != workercompletion.FindingClaimAcceptedResidual || report.Findings[1].Severity != "" {
		t.Fatalf("accepted residual must drop severity, got %+v", report.Findings[1])
	}
	if report.Findings[2].Claim != workercompletion.FindingClaimModel || report.Findings[2].Severity != "" {
		t.Fatalf("model finding = %+v", report.Findings[2])
	}
}

func TestParseWorkerCompletionReportOptionalEvidenceHint(t *testing.T) {
	raw := `{"leg_status":"complete","findings":[{"path":"f.go","evidence":"read#1","line":1,"excerpt":"alpha"}]}`
	report, ok := workercompletion.ParseWorkerCompletionReport(raw)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if len(report.Findings) != 1 || report.Findings[0].Path != "f.go" || report.Findings[0].Evidence != "read#1" {
		t.Fatalf("findings = %+v", report.Findings)
	}
}

func TestNormalizeFindingsDedupesByPath(t *testing.T) {
	report, ok := workercompletion.ParseWorkerCompletionReport(`{"leg_status":"complete","findings":[{"path":"a.go","line":1,"excerpt":"x"},{"path":"a.go","line":1,"excerpt":"x"},{"path":"b.go","note":"note-only"}]}`)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if len(report.Findings) != 2 {
		t.Fatalf("findings = %+v want 2 after dedupe", report.Findings)
	}
}

func TestLastCompleteLegReportReadsLatestSuccess(t *testing.T) {
	report, _, ok := workercompletion.LastCompleteLegReport([]api.Message{
		{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				Tool:     "complete_leg",
				ToolArgs: map[string]any{"leg_status": "partial", "brief": "first"},
				Outcome:  api.ToolResultOutcomeCompleted,
			},
		},
		{
			Role: api.MessageRoleTool,
			ToolResult: &api.ToolResult{
				Tool:     "complete_leg",
				ToolArgs: map[string]any{"leg_status": "complete", "brief": "second"},
				Outcome:  api.ToolResultOutcomeCompleted,
			},
		},
	})
	if !ok || report.LegStatus != "complete" || report.Brief != "second" {
		t.Fatalf("report = %+v ok=%v", report, ok)
	}
}

func TestLastCompleteLegReportSkipsRejected(t *testing.T) {
	if _, _, ok := workercompletion.LastCompleteLegReport([]api.Message{{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			Tool:     "complete_leg",
			ToolArgs: map[string]any{"leg_status": "complete", "brief": "nope"},
			Outcome:  api.ToolResultOutcomeRejected,
		},
	}}); ok {
		t.Fatal("rejected complete_leg must not count")
	}
}

func TestLastCompleteLegReportSurvivesCompactedResultBody(t *testing.T) {
	compacted := "[compacted tool_result — original ~2240 tokens; map + verbatim head/tail below are the working set]\n{\"brief\":\"Triaged the"
	report, _, ok := workercompletion.LastCompleteLegReport([]api.Message{{
		Role:    api.MessageRoleTool,
		Content: compacted,
		ToolResult: &api.ToolResult{
			Tool:    "complete_leg",
			Content: compacted,
			ToolArgs: map[string]any{
				"leg_status": "complete",
				"brief":      "Triaged the three security scans.",
				"findings": []any{map[string]any{
					"path":    "src/coropa/mcp/primitives.py",
					"line":    113,
					"excerpt": "exec(src, ns)",
				}},
			},
			Outcome: api.ToolResultOutcomeCompleted,
		},
	}})
	if !ok || report.LegStatus != "complete" || report.Brief != "Triaged the three security scans." {
		t.Fatalf("report = %+v ok=%v", report, ok)
	}
	if len(report.Findings) != 1 || report.Findings[0].Path != "src/coropa/mcp/primitives.py" {
		t.Fatalf("findings = %+v", report.Findings)
	}
}

func TestLastCompleteLegReportIgnoresResultBody(t *testing.T) {
	if _, _, ok := workercompletion.LastCompleteLegReport([]api.Message{{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			Tool:    "complete_leg",
			Content: `{"leg_status":"complete","brief":"body only"}`,
			Outcome: api.ToolResultOutcomeCompleted,
		},
	}}); ok {
		t.Fatal("result body must not be parsed as the report")
	}
}

func TestReportFromCompleteLegArgsRejectsInvalidStatus(t *testing.T) {
	for _, args := range []map[string]any{
		nil,
		{},
		{"brief": "no status"},
		{"leg_status": "completed", "brief": "alias"},
	} {
		if _, ok := workercompletion.ReportFromCompleteLegArgs(args); ok {
			t.Fatalf("args %v must not decode", args)
		}
	}
}

func TestWorkerReportPreservesEvaluationInputAndBoundsOnlyParentProjection(t *testing.T) {
	brief := strings.Repeat("é", 1300)
	narrative := strings.Repeat("risk ", 80)
	excerpt := strings.Repeat("x", 400)
	report, ok := workercompletion.ParseWorkerCompletionReport(`{"leg_status":"complete","brief":"` + brief + `","objectives_met":["` + narrative + `"],"findings":[{"path":"a.go","line":1,"excerpt":"` + excerpt + `"}]}`)
	if !ok {
		t.Fatal("report failed to parse")
	}
	parent := report.ParentReport()
	if report.Brief != brief || report.ObjectivesMet[0] != strings.TrimSpace(narrative) || report.Findings[0].Excerpt != excerpt {
		t.Fatalf("presentation erased evaluation input: %+v", report)
	}
	if len([]rune(parent.Brief)) > 1200 || !strings.HasSuffix(parent.Brief, "…") || len([]rune(parent.ObjectivesMet[0])) > 200 {
		t.Fatalf("parent presentation was not bounded: %+v", parent)
	}
	if parent.Findings[0].Excerpt != "" || parent.Findings[0].Path != "a.go" || parent.Findings[0].Line != 1 {
		t.Fatalf("parent invented an excerpt or lost its location: %+v", parent.Findings)
	}
}

func TestParseWorkerCompletionReportRejectsAliasStatus(t *testing.T) {
	for _, status := range []string{"completed", "in_progress", "in progress", "done"} {
		if _, ok := workercompletion.ParseWorkerCompletionReport(`{"leg_status":"` + status + `","brief":"done"}`); ok {
			t.Fatalf("alias %q must not parse", status)
		}
	}
}

func TestParseWorkerCompletionReportRejectsHybrid(t *testing.T) {
	t.Parallel()
	raw := `{"leg_status":"complete","brief":"done"}`
	if _, ok := workercompletion.ParseWorkerCompletionReport(raw); !ok {
		t.Fatal("expected bare json envelope")
	}
	if _, ok := workercompletion.ParseWorkerCompletionReport("Summary\n" + raw); ok {
		t.Fatal("hybrid prose must not parse")
	}
}

func TestEnrichWorkerCompletionReportFilesModifiedIsHostAuthoritative(t *testing.T) {
	report := workercompletion.WorkerCompletionReport{
		LegStatus:     "complete",
		FilesModified: []string{"src/a.go"},
		Brief:         "ok",
	}
	proof := workercompletion.WorkerCompletionProof{ChangedPaths: []string{"src/b.go"}}
	out := workercompletion.EnrichWorkerCompletionReport(report, proof, "complete")
	if len(out.FilesModified) != 1 || out.FilesModified[0] != "src/b.go" {
		t.Fatalf("files = %v want exactly host proof [src/b.go]", out.FilesModified)
	}
	if out.LegStatus != "complete" || len(out.EvidenceObligations) != 1 ||
		out.EvidenceObligations[0].Status != workercompletion.EvidenceStatusUnmet {
		t.Fatalf("unverified report = %+v", out)
	}
}

func TestEnrichWorkerCompletionReportProjectsWorkerState(t *testing.T) {
	for state, want := range map[string]string{
		"complete":       "complete",
		"open":           "complete",
		"partial":        "partial",
		"failed":         "partial",
		"canceled":       "partial",
		"needs_decision": "blocked",
		"held":           "blocked",
		"unknown":        "",
	} {
		out := workercompletion.EnrichWorkerCompletionReport(
			workercompletion.WorkerCompletionReport{}, workercompletion.WorkerCompletionProof{}, state,
		)
		if out.LegStatus != want {
			t.Fatalf("state %q leg_status = %q want %q", state, out.LegStatus, want)
		}
	}
}

func TestEnrichWorkerCompletionReportDischargesVerificationFromHostReceipt(t *testing.T) {
	report := workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "done"}
	proof := workercompletion.WorkerCompletionProof{
		ChangedPaths: []string{"src/a.go"}, SourceRevision: "boot:2", SourceRootDigest: "root",
		InvocationReceipts: []workercompletion.WorkerInvocationReceipt{{
			ID: "receipt-1", Tool: "verify", Status: api.InvocationStatusCompleted,
			EvidenceKind: "result", EvidenceRef: "message-1", Verdict: "passed",
			SourceRevision: "boot:2", SourceRootDigest: "root",
		}},
	}
	out := workercompletion.EnrichWorkerCompletionReport(report, proof, "complete")
	if out.LegStatus != "complete" || len(out.EvidenceObligations) != 1 {
		t.Fatalf("verified report = %+v", out)
	}
	obligation := out.EvidenceObligations[0]
	if obligation.Status != workercompletion.EvidenceStatusSatisfied || len(obligation.EvidenceRefs) != 2 {
		t.Fatalf("obligation = %+v", obligation)
	}
}

func TestEnrichWorkerCompletionReportRejectsStaleVerification(t *testing.T) {
	out := workercompletion.EnrichWorkerCompletionReport(
		workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "done"},
		workercompletion.WorkerCompletionProof{
			ChangedPaths: []string{"src/a.go"}, SourceRevision: "boot:3", SourceRootDigest: "root",
			InvocationReceipts: []workercompletion.WorkerInvocationReceipt{{
				ID: "receipt-1", Tool: "verify", Status: api.InvocationStatusCompleted, Verdict: "passed",
				SourceRevision: "boot:2", SourceRootDigest: "root",
			}},
		}, "complete",
	)
	if out.LegStatus != "complete" || out.EvidenceObligations[0].Status != workercompletion.EvidenceStatusUnmet {
		t.Fatalf("stale verify satisfied obligation: %+v", out)
	}
}

func TestEnrichWorkerCompletionReportDoesNotTreatFailedVerifyAsSatisfied(t *testing.T) {
	out := workercompletion.EnrichWorkerCompletionReport(
		workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "done"},
		workercompletion.WorkerCompletionProof{
			ChangedPaths: []string{"src/a.go"},
			InvocationReceipts: []workercompletion.WorkerInvocationReceipt{{
				ID: "receipt-1", Tool: "verify", Status: api.InvocationStatusCompleted, Verdict: "failed",
			}},
		},
		"complete",
	)
	if out.LegStatus != "complete" || out.EvidenceObligations[0].Status != workercompletion.EvidenceStatusUnmet {
		t.Fatalf("failed verify satisfied obligation: %+v", out)
	}
}

func TestEnrichWorkerCompletionReportAcceptsCommandWhenAllowed(t *testing.T) {
	declaredCommand := ""
	out := workercompletion.EnrichWorkerCompletionReport(
		workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "done"},
		workercompletion.WorkerCompletionProof{
			ChangedPaths: []string{"src/a.go"}, SourceRevision: "boot:2", SourceRootDigest: "root",
			DeclaredCommand: &declaredCommand,
			InvocationReceipts: []workercompletion.WorkerInvocationReceipt{{
				ID: "receipt-1", Tool: "command", IsCheck: true, Status: api.InvocationStatusCompleted, Verdict: "passed",
				SourceRevision: "boot:2", SourceRootDigest: "root",
			}},
		}, "complete",
	)
	if out.LegStatus != "complete" || out.EvidenceObligations[0].Status != workercompletion.EvidenceStatusSatisfied {
		t.Fatalf("command receipt should satisfy undeclared proof: %+v", out)
	}
}

func TestEnrichWorkerCompletionReportIgnoresCommandWhenVerifyOnly(t *testing.T) {
	out := workercompletion.EnrichWorkerCompletionReport(
		workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "done"},
		workercompletion.WorkerCompletionProof{
			ChangedPaths: []string{"src/a.go"}, SourceRevision: "boot:2", SourceRootDigest: "root",
			InvocationReceipts: []workercompletion.WorkerInvocationReceipt{{
				ID: "receipt-1", Tool: "command", Status: api.InvocationStatusCompleted, Verdict: "passed",
				SourceRevision: "boot:2", SourceRootDigest: "root",
			}},
		}, "complete",
	)
	if out.EvidenceObligations[0].Status != workercompletion.EvidenceStatusUnmet {
		t.Fatalf("verify-only proof must ignore command: %+v", out)
	}
}
