package sourceledger

import (
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Opening a window waits for its start inventory however long that takes, so a
// slow machine still attributes the command's changes to its window.
func TestSlowWindowStartStillAttributesTheCommand(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "out.txt", "before\n")
	real := store.Commands.inventory
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	store.Commands.inventory = &gatedInventory{commandsInventoryPort: real, release: release, entered: entered}

	type opened struct {
		window *OpenCommandWindow
		err    error
	}
	done := make(chan opened, 1)
	go func() {
		window, err := store.Commands.OpenCommandWindow(ctx, CommandWindowInput{
			ProjectID: "p1", Roots: onDiskRoots(root), SessionID: "s1", Turn: 3,
			ToolCallID: "call-1", ToolName: "command", CommandLine: "make",
		})
		done <- opened{window, err}
	}()
	testutil.Receive(t, "the start inventory is held", entered)
	close(release)
	got := testutil.Receive(t, "the window opens", done)
	testutil.FailErr(t, "open a window behind a slow inventory", got.err)
	store.Commands.inventory = real

	writeRootFile(t, root, "out.txt", "after\n")
	deliverWrites(ctx, root, "out.txt")
	closeAndSettle(t, ctx, got.window)
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query walk", err)
	if effect := effectsByPath(walk)["out.txt"]; effect.CommandWindowID != got.window.ID || effect.Cause != CauseCommandWindow {
		t.Fatalf("command's change = %+v, want window %s", effect, got.window.ID)
	}
}
