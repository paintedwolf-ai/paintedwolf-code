package native

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type partialCaptureResources struct{ *toolcontract.ProgramMemory }

func (q partialCaptureResources) ReserveSpill(ctx context.Context, n int64) (func(), error) {
	return q.ReserveProgramState(ctx, n)
}

func partialEffectsCapture(t *testing.T, recorder *invocation.SQLRecorder, program, session, project string) invocation.ProgramInputCapture {
	t.Helper()
	quota := partialCaptureResources{programDoorMemory(t)}
	outputs, err := invocation.NewProgramOutputRetention(t.Context(), recorder, blobstore.Store{Root: t.TempDir()}, session, quota)
	testutil.FailErr(t, "admit input lifetime", err)
	testutil.FailErr(t, "restore input lifetime", outputs.Restore(t.Context()))
	return invocation.ProgramInputCapture{Outputs: outputs, State: quota, Projector: captureprojection.New(secretmatch.NewInertMatcher(), nil), ProgramID: program, Scope: captureprojection.Scope{RootSessionID: session, SessionID: session, ProjectID: project}, Screening: captureprojection.StreamOptions{WorkingMemory: quota, Bounds: secretmatch.StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 30}}, MaximumNodes: 1024, MaximumDepth: 64}
}
