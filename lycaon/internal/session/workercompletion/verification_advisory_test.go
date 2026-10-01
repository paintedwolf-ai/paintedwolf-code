package workercompletion_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestVerificationFailureDoesNotDowngradeDeliveredWork(t *testing.T) {
	proof := workercompletion.WorkerCompletionProof{
		ChangedPaths: []string{"material"}, SourceRevision: "content", SourceRootDigest: "root",
		Verification: &verification.Assessment{Method: verification.Blocked, Reason: "Required service is unavailable."},
	}
	report := workercompletion.EnrichWorkerCompletionReport(workercompletion.WorkerCompletionReport{LegStatus: "complete"}, proof, "complete")
	if report.LegStatus != "complete" || report.SuggestedNextTask != "" {
		t.Fatalf("report forced more work: %+v", report)
	}
	if len(report.EvidenceObligations) != 1 || report.EvidenceObligations[0].Status != "unmet" {
		t.Fatalf("validation limitation disappeared: %+v", report)
	}
}

func TestVerificationTerminalJoinReplacesLaunchWithoutDuplication(t *testing.T) {
	proof := workercompletion.WorkerCompletionProof{InvocationReceipts: []workercompletion.WorkerInvocationReceipt{
		{ID: "launch", CheckID: "call", Tool: "verify", Status: api.InvocationStatusCompleted},
	}}
	terminal := workercompletion.WorkerInvocationReceipt{ID: "terminal", CheckID: "call", Tool: "verify", Status: api.InvocationStatusCompleted, Verdict: "passed", SourceRevision: "launch-content"}
	proof = proof.WithSourceRuns([]workercompletion.WorkerInvocationReceipt{terminal, terminal})
	if len(proof.InvocationReceipts) != 1 || proof.InvocationReceipts[0].Verdict != "passed" || proof.InvocationReceipts[0].SourceRevision != "launch-content" {
		t.Fatalf("terminal projection=%+v", proof.InvocationReceipts)
	}
}

func TestVerificationLatestFailureRemainsVisible(t *testing.T) {
	proof := workercompletion.WorkerCompletionProof{ChangedPaths: []string{"material"}, SourceRevision: "content", SourceRootDigest: "root",
		InvocationReceipts: []workercompletion.WorkerInvocationReceipt{
			{ID: "old", Tool: "verify", Command: "check  all", Status: api.InvocationStatusCompleted, Verdict: "passed", SourceRevision: "content", SourceRootDigest: "root"},
			{ID: "latest", Tool: "verify", Command: "check all", Status: api.InvocationStatusCompleted, Verdict: "failed", SourceRevision: "content", SourceRootDigest: "root"},
		},
	}
	status, _, _ := workercompletion.SourceEvidenceView(proof)
	if status != workercompletion.EvidenceStatusUnmet {
		t.Fatalf("old pass hid latest failure: %s", status)
	}
}

func TestVerificationOrdinaryCommandIsNotCheckEvidence(t *testing.T) {
	declared := ""
	proof := workercompletion.WorkerCompletionProof{DeclaredCommand: &declared, ChangedPaths: []string{"material"}, SourceRevision: "content", SourceRootDigest: "root",
		InvocationReceipts: []workercompletion.WorkerInvocationReceipt{{ID: "ordinary", Tool: "command", Command: "inspect-state", Status: api.InvocationStatusCompleted, Verdict: "passed", SourceRevision: "content", SourceRootDigest: "root"}},
	}
	status, _, _ := workercompletion.SourceEvidenceView(proof)
	if status != workercompletion.EvidenceStatusUnmet {
		t.Fatal("ordinary command fabricated test evidence")
	}
	proof.InvocationReceipts[0].IsCheck = true
	status, _, _ = workercompletion.SourceEvidenceView(proof)
	if status != workercompletion.EvidenceStatusSatisfied {
		t.Fatal("nominated check did not count")
	}
}
