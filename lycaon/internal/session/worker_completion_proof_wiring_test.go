package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Overlay diffs retain their source generation in completion proof.
func TestOverlayChangedPathsFeedWorkerProof(t *testing.T) {
	dir := t.TempDir()
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"space_pacifism/main.py"}}
	baseline := testbaseline.Capture(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "space_pacifism"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "space_pacifism/main.py"), []byte("new\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	task := &api.WorkerTask{
		AgentType: "implementer",
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir,
		WorkspaceRoot:         dir, // claimed branch (test uses same tree)
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	changed := session.OverlayWorkspaceChangedPaths(t.Context(), task, nil)
	proof := workercompletion.BuildWorkerCompletionProof(nil, changed, workercompletion.SourceRevision{
		Revision: "revision-1", RootDigest: "root-1",
	})
	if len(proof.ChangedPaths) != 1 || proof.ChangedPaths[0] != "space_pacifism/main.py" {
		t.Fatalf("ChangedPaths = %v, want [space_pacifism/main.py]", proof.ChangedPaths)
	}
	if proof.SourceRevision != "revision-1" || proof.SourceRootDigest != "root-1" {
		t.Fatalf("source generation = %q/%q", proof.SourceRevision, proof.SourceRootDigest)
	}
}

func TestFormatWorkerCompletionEnvelopeIncludesProofAndReportJSON(t *testing.T) {
	out := session.FormatWorkerCompletionEnvelope(session.WorkerCompletionEnvelope{
		JobID:   "job-1",
		State:   "complete",
		Summary: "done",
		Body:    "done",
		Digest:  "Host worker digest",
		Proof: workercompletion.WorkerCompletionProof{
			ChangedPaths: []string{"src/a.go"},
			ReceiptCount: 2,
		},
		Report: workercompletion.WorkerCompletionReport{
			LegStatus:     "complete",
			FilesModified: []string{"src/a.go"},
			ObjectivesMet: []string{"added handler"},
			Brief:         "done",
		},
	})
	if strings.Contains(out, "<digest>") {
		t.Fatalf("unexpected <digest>: %q", out)
	}
	if strings.Contains(out, "<task_result>") {
		t.Fatalf("task_result must be omitted when it equals summary: %q", out)
	}
	if !strings.Contains(out, "<proof_json>") || !strings.Contains(out, "src/a.go") {
		t.Fatalf("missing proof_json: %q", out)
	}
	if !strings.Contains(out, "<report_json>") || !strings.Contains(out, "objectives_met") {
		t.Fatalf("missing report_json: %q", out)
	}
}
