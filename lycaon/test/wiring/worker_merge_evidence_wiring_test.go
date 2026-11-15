package wiring

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// The production merge service evaluates a finished worker against its workspace observations, so
// a preview reports the source revision its evidence was judged at.
func TestOverlayPreviewEvidenceUsesWorkspaceObservations(t *testing.T) {
	mock := &workerScriptMock{script: []api.ToolCall{commandCall("write", `printf 'package main\n// edited\n' > main.go`)}, results: map[string]string{}}
	h := BuildForTest(t, WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, dir)
	testutil.FailErr(t, "seed main.go", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create parent", err)

	task := api.WorkerTask{ParentSessionID: parent.ID, Prompt: "Change main.go.", Brief: "fixture", AgentType: orchestration.ProfileImplementer, Scope: writeScope()}
	testutil.FailErr(t, "worker defaults", worker.ApplyEnqueueDefaults(
		&task, project.ProjectScope{ProjectID: parent.ProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig(),
	))
	jobID, err := h.Delegations.Queue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue worker", err)
	testutil.FailErr(t, "drain worker", DrainPendingWorkerJobs(ctx, h, parent.ProjectID, ""))

	raw, err := h.ToolRegistry.Run(ctx, "preview_overlay", map[string]any{"overlay_id": jobID}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: parent.ID, Agent: "coordinator"},
	})
	testutil.FailErr(t, "preview_overlay", err)
	var preview api.WorkerMergeResult
	testutil.FailErr(t, "decode preview", json.Unmarshal([]byte(raw), &preview))
	if preview.SourceEvidence == nil || preview.SourceEvidence.SourceRevision == "" {
		t.Fatalf("source evidence = %+v, want one judged at the overlay's source revision", preview.SourceEvidence)
	}
}
