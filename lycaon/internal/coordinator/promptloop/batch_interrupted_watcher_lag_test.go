package promptloop

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestCompleteLegSettleToolResultFailsClosedOnWatcherLag verifies that
// batch_interrupted.go's complete_leg settlement does not stamp a stale
// pre-edit source revision while watcher delivery is held back, and stamps the
// updated revision once the watcher settles.
func TestCompleteLegSettleToolResultFailsClosedOnWatcherLag(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	repochangetest.Watch(t, root)
	repochangetest.Acquire(t)

	gatePath := filepath.Join(root, "gate.go")
	testutil.FailErr(t, "write initial gate.go", os.WriteFile(gatePath, []byte("package gate\n\nfunc V() bool { return true }\n"), 0o600))

	ledger := sourceledger.New(testdbfixture.Open(t, "store.db"), t.TempDir(), sourceledger.Timing{})
	t.Cleanup(func() { _ = ledger.Snapshots.Close() })

	initRev, initRoot := sourceledger.SettledVerificationState(ctx, ledger.Inventory, root)
	if initRev == "" || initRoot == "" {
		t.Fatalf("initial SettledVerificationState empty: rev=%q root=%q", initRev, initRoot)
	}

	batch := &toolBatch{Tools: &toolInvocations{}}
	tc := api.ToolCall{ID: "call-complete-leg", Name: "complete_leg"}
	toolCtx := tools.ToolContext{
		Source: tools.InvocationSource{WorkerBranchRoot: root, Observations: ledger.Inventory},
	}
	msg := api.Message{ID: "msg-complete-leg"}

	restoreDebounce := repochange.UseDebounce(ctx, 5*time.Second)
	restored := false
	restore := func() {
		if !restored {
			restored = true
			restoreDebounce()
		}
	}
	t.Cleanup(restore)

	testutil.FailErr(t, "external edit gate.go", os.WriteFile(gatePath, []byte("package gate\n\nfunc V() bool { return false }\n"), 0o600))

	settledLag, err := batch.settleToolResult(ctx, tc, toolCtx, msg, toolInvocation{invoked: true})
	testutil.FailErr(t, "settleToolResult during lag", err)
	if settledLag.sourceRevision != "" || settledLag.sourceRootDigest != "" {
		t.Fatalf("complete_leg stamped revision during watcher lag: rev=%q root=%q (init=%q)",
			settledLag.sourceRevision, settledLag.sourceRootDigest, initRev)
	}

	restore()
	settledAfter, err := batch.settleToolResult(ctx, tc, toolCtx, msg, toolInvocation{invoked: true})
	testutil.FailErr(t, "settleToolResult after flush", err)
	if settledAfter.sourceRevision == "" || settledAfter.sourceRevision == initRev || settledAfter.sourceRootDigest != initRoot {
		t.Fatalf("complete_leg after watcher flush = (rev=%q root=%q), want new revision on %q",
			settledAfter.sourceRevision, settledAfter.sourceRootDigest, initRoot)
	}
}
