package invocation_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func (f programReceiptFixture) capture(t *testing.T) invocation.ProgramInputCapture {
	t.Helper()
	return f.captureAt(t, t.TempDir())
}

func (f programReceiptFixture) captureAt(t *testing.T, root string) invocation.ProgramInputCapture {
	t.Helper()
	quota := &inputCaptureQuota{}
	outputs, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, blobstore.Store{Root: root}, f.session, quota)
	testutil.FailErr(t, "admit input lifetime", err)
	testutil.FailErr(t, "restore input lifetime", outputs.Restore(t.Context()))
	t.Cleanup(func() {
		_, err := f.database.ExecContext(context.Background(), `DELETE FROM invocation_programs WHERE id=?`, f.program)
		testutil.FailErr(t, "release capture fixture references", err)
		testutil.FailErr(t, "close capture fixture lifetime", outputs.Close(context.Background()))
		if quota.memory != 0 || quota.disk != 0 || quota.state != 0 || quota.detector != 0 {
			t.Fatalf("capture fixture leaked resources: %+v", quota)
		}
	})
	return invocation.ProgramInputCapture{Outputs: outputs, State: quota, Projector: captureprojection.New(secretmatch.NewInertMatcher(), nil), ProgramID: f.program, Scope: captureprojection.Scope{RootSessionID: f.session, SessionID: f.session}, Screening: captureprojection.StreamOptions{WorkingMemory: quota, Bounds: secretmatch.StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 30}}, MaximumNodes: 1024, MaximumDepth: 64}
}
