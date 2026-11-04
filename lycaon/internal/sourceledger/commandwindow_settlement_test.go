package sourceledger

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
)

// beforePassCompletes runs before inside a pass, after it has captured the tree.
type beforePassCompletes struct {
	inventoryCommandsPort
	before func()
}

func (p beforePassCompletes) notePassCompleted(ctx context.Context, projectID string, roots []RootSpec) {
	p.before()
	p.inventoryCommandsPort.notePassCompleted(ctx, projectID, roots)
}

// A pass that captured before the last write says nothing about it, so the close
// still reconciles what landed after that capture.
func TestPassInFlightAtCloseDoesNotLoseTrailingOutput(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	repochange.MarkCoverageCompleteForTest(root)
	t.Cleanup(func() { repochange.CloseRoot(root) })
	writeRootFile(t, root, "seed.txt", "seed\n")
	window := openTestWindow(t, store, ctx, root, "./gen.sh")
	store.Commands.windowSettle = time.Hour
	store.Inventory.commands = beforePassCompletes{store.Inventory.commands, sync.OnceFunc(func() {
		writeRootFile(t, root, "gen.txt", "generated\n")
		deliverWrites(ctx, root, "gen.txt")
		testutil.FailErr(t, "close during the pass", window.Close(ctx))
	})}
	forceTestPass(t, store, ctx, root)
	testutil.AwaitClosed(t, "the window settles", window.Settled())
	if !window.IsSettled() {
		t.Fatal("window did not settle")
	}
	if effect := newestEffects(t, store, ctx)["gen.txt"]; effect.CommandWindowID != window.ID {
		t.Fatalf("trailing output = %+v, want the window", effect)
	}
}

// A pass on another branch observed another tree.
func TestPassOnAnotherBranchDoesNotEndTheWindowsWait(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	repochange.MarkCoverageCompleteForTest(root)
	t.Cleanup(func() { repochange.CloseRoot(root) })
	writeRootFile(t, root, "seed.txt", "seed\n")
	window := openTestWindow(t, store, ctx, root, "./gen.sh")
	store.Commands.windowSettle = time.Hour

	writeRootFile(t, root, "gen.txt", "generated\n")
	deliverWrites(ctx, root, "gen.txt")
	testutil.FailErr(t, "close window", window.Close(ctx))
	other := t.TempDir()
	testutil.FailErr(t, "write other tree", os.WriteFile(filepath.Join(other, "other.txt"), []byte("other\n"), 0o600))
	testutil.FailErr(t, "pass on another branch", store.Inventory.EnsureInventory(ctx, InventoryRequest{
		ProjectID: "p1", Roots: []RootSpec{{ID: "r1", BranchID: sourcebranch.ID("worktree:other"), Path: other}}, Force: true, Wait: true,
	}))

	testutil.AwaitClosed(t, "the window settles", window.Settled())
	if effect := newestEffects(t, store, ctx)["gen.txt"]; effect.CommandWindowID != window.ID {
		t.Fatalf("output = %+v, want the window", effect)
	}
}
