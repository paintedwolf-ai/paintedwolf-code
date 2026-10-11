package workercompletion_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatWorkerCompletionEnvelope(t *testing.T) {
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:          "job-1",
		ChildSessionID: "child-1",
		AgentType:      "implementer",
		State:          "partial",
		HintCode:       "WORKER_SUMMARY_NO_ARTIFACT",
		Body:           "no disk proof",
	})
	if !strings.Contains(out, `job_id="job-1"`) {
		t.Fatalf("out = %q", out)
	}
	if !strings.Contains(out, `state="partial"`) {
		t.Fatalf("out = %q", out)
	}
	if !strings.Contains(out, "<task_result>") || !strings.Contains(out, "no disk proof") {
		t.Fatalf("out = %q", out)
	}
}

func TestFormatWorkerCompletionEnvelopeIncludesMergeStatus(t *testing.T) {
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:       "job-2",
		State:       "complete",
		MergeStatus: "pending",
	})
	if !strings.Contains(out, `merge_status="pending"`) {
		t.Fatalf("out = %q", out)
	}
	if strings.Contains(out, "workspace_root") {
		t.Fatalf("completion XML must not name a workspace_root: %q", out)
	}
}

func TestFormatWorkerCompletionEnvelopeScrubsWorkerBranchPaths(t *testing.T) {
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID: "job-3",
		State: "complete",
		Body:  "built at /Users/me/.config/paintedwolf-dev/worker-branches/5d960b8f0a1ff696/582d661b-6b3c-4018-a646-0e1468aeb7e6/src/main.rs",
	})
	if strings.Contains(out, "worker-branches") || strings.Contains(out, "paintedwolf") {
		t.Fatalf("completion XML leaked a worker-branch path: %q", out)
	}
	if !strings.Contains(out, "src/main.rs") {
		t.Fatalf("scrubbed body should keep the repo-relative suffix: %q", out)
	}
}

func TestFormatWorkerCompletionEnvelopeOpenState(t *testing.T) {
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:       "job-3",
		State:       "open",
		MergeStatus: "pending",
		AgentType:   "implementer",
	})
	if !strings.Contains(out, `state="open"`) {
		t.Fatalf("out = %q", out)
	}
	if !strings.Contains(out, "changes open on branch") {
		t.Fatalf("out = %q", out)
	}
}

func TestFormatWorkerCompletionEnvelopeStateSummaries(t *testing.T) {
	for state, want := range map[string]string{
		"partial": "finished with remaining work",
		"held":    "is held",
		"failed":  "failed",
	} {
		out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
			JobID: "job-3", ChildSessionID: "child-3", AgentType: "implementer", State: state,
		})
		if !strings.Contains(out, want) {
			t.Fatalf("state %q out = %q", state, out)
		}
	}
}

func TestNormalizeWorkerCompletionStateUsesClosedVocabulary(t *testing.T) {
	for input, want := range map[string]string{
		"complete":       "complete",
		"partial":        "partial",
		"open":           "open",
		"needs_decision": "needs_decision",
		"held":           "held",
		"canceled":       "canceled",
		"failed":         "failed",
		"completed":      "",
		"error":          "",
		"unknown":        "",
	} {
		if got := workercompletion.NormalizeWorkerCompletionState(input); got != want {
			t.Fatalf("state %q = %q want %q", input, got, want)
		}
	}
}

func TestFormatWorkerCompletionEnvelopeRejectsInvalidState(t *testing.T) {
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID: "job-3", ChildSessionID: "child-3", AgentType: "implementer", State: "completed",
	})
	if out != "" {
		t.Fatalf("out = %q want empty", out)
	}
}

func TestFormatWorkerCompletionEnvelopeOmitsReceiptLedger(t *testing.T) {
	declaredCommand := "check"
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:   "job-receipts",
		State:   "complete",
		Summary: "surveyed auth",
		Proof: workercompletion.WorkerCompletionProof{
			ChangedPaths: []string{"internal/auth/token.go"},
			ReceiptCount: 86,
			InvocationReceipts: []workercompletion.WorkerInvocationReceipt{{
				ID:               "6bef141c-dd8d-480c-b428-edb5338297fd",
				Tool:             "grep",
				Status:           api.InvocationStatusCompleted,
				SourceRevision:   "223bb346-4a7d-4325-8023-7855fffc2f54:240",
				SourceRootDigest: "f7945ae9afbd0dfcd5469ff89715fb0e",
			}},
			SourceRevision:   "223bb346-4a7d-4325-8023-7855fffc2f54:240",
			SourceRootDigest: "f7945ae9afbd0dfcd5469ff89715fb0e",
			DeclaredCommand:  &declaredCommand,
		},
		Report: workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "surveyed auth"},
	})
	for _, banned := range []string{
		"invocation_receipts", "6bef141c", "source_revision", "source_root_digest",
		"declared_command", "<digest>", "<task_result>",
	} {
		if strings.Contains(out, banned) {
			t.Fatalf("parent envelope leaked %q: %s", banned, out)
		}
	}
	if !strings.Contains(out, "receipt_count") || !strings.Contains(out, "internal/auth/token.go") {
		t.Fatalf("proof_json missing: %s", out)
	}
	env, ok := workercompletion.ParseWorkerCompletionEnvelope(out)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if len(env.Proof.InvocationReceipts) != 0 || env.Proof.SourceRevision != "" {
		t.Fatalf("parsed receipts/revision: %+v", env.Proof)
	}
	if env.Proof.ReceiptCount != 86 || env.Body != "" || env.Digest != "" {
		t.Fatalf("parsed envelope = %+v", env)
	}
}

func TestFormatWorkerCompletionEnvelopeOmitsTaskResultWhenReportPresent(t *testing.T) {
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:   "job-report",
		State:   "complete",
		Summary: "surveyed auth",
		Body:    "surveyed auth in a longer essay that nearly duplicates the summary",
		Report:  workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "surveyed auth"},
	})
	if strings.Contains(out, "<task_result>") {
		t.Fatalf("report_json already carries the survey: %q", out)
	}
}

func TestFormatWorkerCompletionEnvelopeKeepsDistinctTaskResult(t *testing.T) {
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:    "job-hint",
		State:    "partial",
		HintCode: "WORKER_SUMMARY_NO_ARTIFACT",
		Summary:  "implementer finished",
		Body:     "no disk proof",
	})
	if !strings.Contains(out, "<task_result>") || !strings.Contains(out, "no disk proof") {
		t.Fatalf("distinct body should stay: %q", out)
	}
}

func TestFormatWorkerCompletionEnvelopeOmitsDigestWhenDecisionPresent(t *testing.T) {
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:   "job-dec",
		State:   "needs_decision",
		Summary: "implementer needs a decision: chown cache?",
		Body:    "NEEDS DECISION — chown cache?",
		Digest:  "Host worker digest",
		DecisionRequest: &api.WorkerDecisionRequest{
			WorkerID: "job-dec", Question: "chown cache?", Options: []string{"yes", "no"},
		},
	})
	if strings.Contains(out, "<digest>") {
		t.Fatalf("unexpected <digest>: %q", out)
	}
	if !strings.Contains(out, "<decision_request_json>") {
		t.Fatalf("missing decision_request_json: %q", out)
	}
}

func TestFormatWorkerCompletionEnvelopeKeepsDigestWithoutReport(t *testing.T) {
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:   "job-digest",
		State:   "complete",
		Summary: "done",
		Digest:  "Host worker digest — agent=scout status=complete",
	})
	if !strings.Contains(out, "<digest>Host worker digest") {
		t.Fatalf("digest should stay when report_json is absent: %q", out)
	}
	if strings.Contains(out, "<task_result>") {
		t.Fatalf("empty distinct body should omit task_result: %q", out)
	}
}

func TestParseWorkerCompletionEnvelopeReportJSON(t *testing.T) {
	content := `<task job_id="j-impl" agent_type="implementer" state="complete">
  <report_json>{"leg_status":"complete","brief":"done"}</report_json>
</task>`
	env, ok := workercompletion.ParseWorkerCompletionEnvelope(content)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if env.AgentType != "implementer" {
		t.Fatalf("agent_type = %q", env.AgentType)
	}
	if env.Report.LegStatus != "complete" || env.Report.Brief != "done" {
		t.Fatalf("report = %+v", env.Report)
	}
}
