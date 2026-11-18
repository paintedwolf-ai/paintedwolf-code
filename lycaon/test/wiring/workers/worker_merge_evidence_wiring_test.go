package workers

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// The production merge service evaluates a finished worker against its workspace observations, so
// a preview reports the source revision its evidence was judged at.
func TestOverlayPreviewEvidenceUsesWorkspaceObservations(t *testing.T) {
	f := newWorkerFlow(t, "merge-evidence")
	f.seed("main.go", "package main\n")

	f.scripts.Push("implementer", writes("main.go", "package main\n// edited\n"), delivers("complete", "Edited main.go."))
	f.start("editor", "implementer", "Change main.go.", "main.go")
	f.drain()
	r1 := f.result("editor", 1)

	raw := f.run("preview_overlay", map[string]any{"overlay_id": r1.ResultID})
	var preview api.WorkerMergeResult
	testutil.FailErr(t, "decode preview", json.Unmarshal([]byte(raw), &preview))
	if preview.SourceEvidence == nil || preview.SourceEvidence.SourceRevision == "" {
		t.Fatalf("source evidence = %+v, want one judged at the overlay's source revision", preview.SourceEvidence)
	}
}
