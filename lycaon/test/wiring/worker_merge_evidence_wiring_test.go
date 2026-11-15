package wiring

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// The production merge service evaluates a finished worker against its workspace observations, so
// a preview reports the source revision its evidence was judged at.
func TestOverlayPreviewEvidenceUsesWorkspaceObservations(t *testing.T) {
	jobID, _, h := runWorkerScriptOnHarness(t, orchestration.ProfileImplementer, writeScope(),
		commandCall("write", `printf 'package main\n// edited\n' > main.go`),
	)
	task, ok := h.Delegations.Queue.Get(jobID)
	if !ok {
		t.Fatalf("worker %s not found", jobID)
	}
	raw, err := h.ToolRegistry.Run(context.Background(), "preview_overlay", map[string]any{"overlay_id": jobID}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: task.ParentSessionID, Agent: "coordinator"},
	})
	testutil.FailErr(t, "preview_overlay", err)
	var preview api.WorkerMergeResult
	testutil.FailErr(t, "decode preview", json.Unmarshal([]byte(raw), &preview))
	if preview.SourceEvidence == nil || preview.SourceEvidence.SourceRevision == "" {
		t.Fatalf("source evidence = %+v, want one judged at the overlay's source revision", preview.SourceEvidence)
	}
}
