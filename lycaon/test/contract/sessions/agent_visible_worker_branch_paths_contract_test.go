package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const leakedBranch = "/Users/me/.config/paintedwolf-dev/worker-branches/5d960b8f0a1ff696/582d661b-6b3c-4018-a646-0e1468aeb7e6"

func TestAgentVisibleWorkerBranchPaths_completionXML(t *testing.T) {
	t.Parallel()
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:       "582d661b-6b3c-4018-a646-0e1468aeb7e6",
		State:       "complete",
		MergeStatus: "pending",
		Body:        "compiled at " + leakedBranch + "/src/main.rs",
	})
	if strings.Contains(out, "workspace_root") {
		t.Fatalf("completion XML must not emit workspace_root: %s", out)
	}
	if strings.Contains(out, "worker-branches") || strings.Contains(out, "paintedwolf") {
		t.Fatalf("completion XML leaked a worker-branch path: %s", out)
	}
}

func TestAgentVisibleWorkerCompletionEnvelope_omitsHostLedger(t *testing.T) {
	t.Parallel()
	declaredCommand := "check"
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:   "job-ledger",
		State:   "complete",
		Summary: "surveyed",
		Body:    "surveyed",
		Digest:  "Host worker digest",
		Proof: workercompletion.WorkerCompletionProof{
			ChangedPaths: []string{"src/a.go"},
			ReceiptCount: 12,
			InvocationReceipts: []workercompletion.WorkerInvocationReceipt{{
				ID:               "receipt-1",
				Tool:             "read",
				SourceRevision:   "rev-1",
				SourceRootDigest: "digest-1",
			}},
			SourceRevision:   "rev-1",
			SourceRootDigest: "digest-1",
			DeclaredCommand:  &declaredCommand,
		},
		Report: workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "surveyed"},
	})
	for _, banned := range []string{
		"invocation_receipts", "receipt-1", "source_revision", "source_root_digest",
		"declared_command", "<digest>", "<task_result>",
	} {
		if strings.Contains(out, banned) {
			t.Fatalf("agent wire leaked %q: %s", banned, out)
		}
	}
}

func TestAgentVisibleWorkerBranchPaths_packBoardRoster(t *testing.T) {
	t.Parallel()
	roster := board.BuildWorkerRoster([]api.WorkerTask{{
		ID:            "582d661b-6b3c-4018-a646-0e1468aeb7e6",
		AgentType:     "implementer",
		Status:        api.WorkerStatusComplete,
		MergeStatus:   api.WorkerMergeStatusPending,
		WorkspaceRoot: leakedBranch,
	}}, api.BoardDetailLevelCompact)
	raw, err := json.Marshal(roster)
	contractcheck.FailErr(t, "marshal roster", err)
	body := string(raw)
	if strings.Contains(body, "workspace_root") {
		t.Fatalf("roster JSON must not include workspace_root: %s", body)
	}
	if strings.Contains(body, "worker-branches") {
		t.Fatalf("roster JSON leaked a worker-branch path: %s", body)
	}
}

func TestAgentVisibleWorkerBranchPaths_rewrite(t *testing.T) {
	t.Parallel()
	got := enginepaths.RewriteWorkerBranchPaths("Compiling mdlinter v0.1.0 (" + leakedBranch + ")")
	if strings.Contains(got, "worker-branches") || strings.Contains(got, "paintedwolf") {
		t.Fatalf("rewritten command tail leaked layout: %q", got)
	}
	if !strings.Contains(got, "(.)") {
		t.Fatalf("branch root should display as '.': %q", got)
	}
}
