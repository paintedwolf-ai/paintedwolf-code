package invocation_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func (f programReceiptFixture) capture(t *testing.T) invocation.ProgramInputCapture {
	t.Helper()
	quota := &inputCaptureQuota{}
	outputs, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, blobstore.Store{Root: t.TempDir()}, f.session, quota)
	testutil.FailErr(t, "admit input lifetime", err)
	testutil.FailErr(t, "restore input lifetime", outputs.Restore(t.Context()))
	return invocation.ProgramInputCapture{Outputs: outputs, State: quota, Projector: captureprojection.New(secretmatch.NewInertMatcher(), nil), ProgramID: f.program, Scope: captureprojection.Scope{RootSessionID: f.session, SessionID: f.session}, Screening: captureprojection.StreamOptions{WorkingMemory: quota, Bounds: secretmatch.StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 30}}, MaximumNodes: 1024, MaximumDepth: 64}
}
