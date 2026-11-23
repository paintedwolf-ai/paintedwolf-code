package sourceledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTrackedOutsideChangeKeepsItsActualPreimageACFL18(t *testing.T) {
	for _, capture := range []struct{ state, reason string }{
		{state: "stored"},
		{state: "metadata_only", reason: "content_not_captured"},
	} {
		t.Run(capture.state, func(t *testing.T) {
			store, ctx, root := openLedgerOnDisk(t)
			insertChat(t, store, ctx, "s1", "")
			dir := outsideDir(t)
			const file = "tracked.txt"
			outside := outsideRoot(t, store, ctx)
			rel := outsideRel(t, filepath.Join(dir, file))
			recordOutside(t, store, ctx, outside, "s1", rel, "last retained head\n")
			head, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, outside, rel)
			testutil.FailErr(t, "resolve the existing file", err)
			const before = "unobserved edit before launch\n"
			writeRootFile(t, dir, file, before)
			if capture.reason != "" {
				store.Commands.retention.files = 0
			}
			window := openOutsideWindow(t, store, ctx, root, dir)
			writeRootFile(t, dir, file, "command result\n")
			closeAndSettle(t, ctx, window)

			var fileID, sha, state, reason string
			err = store.sqlDB.QueryRowContext(ctx, `SELECT e.file_id, v.content_sha256, v.capture_state, v.capture_reason
				FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
				JOIN source_versions v ON v.id = e.before_version_id WHERE o.command_window_id = ?`, window.ID).
				Scan(&fileID, &sha, &state, &reason)
			testutil.FailErr(t, "read the observed preimage", err)
			if fileID != head.FileID || sha != sourceblob.ContentSHA([]byte(before)) || state != capture.state || reason != capture.reason {
				t.Fatalf("observed preimage = %s %s %s/%s", fileID, sha, state, reason)
			}
		})
	}
}

func TestOutsideComparisonKeepsUncoveredPathsOutOfHistory(t *testing.T) {
	for _, phase := range []string{"baseline", "settlement"} {
		t.Run(phase, func(t *testing.T) {
			store, ctx, root := openLedgerOnDisk(t)
			insertChat(t, store, ctx, "s1", "")
			dir := outsideDir(t)
			outside := outsideRoot(t, store, ctx)
			installSnapshots(store, &faultySnapshots{
				scope: boundedScope(sandbox.SurveyBudgets{DirectoryEntries: 4}),
			})
			writeRootFile(t, dir, "limited/unchanged.txt", "unchanged\n")
			writeRootFile(t, dir, "covered.txt", "before\n")
			if phase == "baseline" {
				writeEntries(t, dir, "limited", 4)
			}
			window := openOutsideWindow(t, store, ctx, root, dir)
			if phase == "baseline" {
				for _, name := range []string{"f0.txt", "f1.txt", "f2.txt", "f3.txt"} {
					testutil.FailErr(t, "remove budget fixture", os.Remove(filepath.Join(dir, "limited", name)))
				}
			} else {
				writeEntries(t, dir, "limited", 4)
			}
			writeRootFile(t, dir, "covered.txt", "after\n")
			closeAndSettle(t, ctx, window)

			changes := effectsByPath(sessionWalk(t, store, ctx))
			if _, ok := changes[outsideRel(t, filepath.Join(dir, "covered.txt"))]; !ok || len(changes) != 1 {
				t.Fatalf("changes = %+v, want only covered.txt", changes)
			}
			got := storedCoverage(t, store, ctx, window.ID)[outsideKey(outside, outsideRel(t, dir))]
			want := CoverageBoundary{Path: "limited", Reason: api.SourceWindowBoundaryDirectoryCap}
			if got.State != api.SourceWindowCoveragePartial || len(got.Boundaries) != 1 || got.Boundaries[0] != want {
				t.Fatalf("coverage = %+v, want partial at %+v", got, want)
			}
		})
	}
}

func TestOutsideComparisonRefusesReplacedRoot(t *testing.T) {
	for _, replacement := range []string{"directory", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			store, ctx, root := openLedgerOnDisk(t)
			insertChat(t, store, ctx, "s1", "")
			outside := outsideRoot(t, store, ctx)
			parent := outsideDir(t)
			dir := filepath.Join(parent, "observed")
			writeRootFile(t, parent, "observed/old.txt", "observed\n")
			fault := installSnapshots(store, &faultySnapshots{})
			window := openOutsideWindow(t, store, ctx, root, dir)
			testutil.FailErr(t, "move observed directory", os.Rename(dir, filepath.Join(parent, "old")))
			if replacement == "symlink" {
				writeRootFile(t, parent, "other/new.txt", "unrelated\n")
				testutil.FailErr(t, "replace observed directory with a link", os.Symlink(filepath.Join(parent, "other"), dir))
			} else {
				writeRootFile(t, parent, "observed/new.txt", "unrelated\n")
			}
			closeAndSettle(t, ctx, window)

			if fault.captures() != 1 {
				t.Fatalf("captures = %d, want the original baseline alone", fault.captures())
			}
			if changes := effectsByPath(sessionWalk(t, store, ctx)); len(changes) != 0 {
				t.Fatalf("changes = %+v, want none for a replacement root", changes)
			}
			got := storedCoverage(t, store, ctx, window.ID)[outsideKey(outside, outsideRel(t, dir))]
			if got.State != api.SourceWindowCoverageNotObserved || got.Reason != api.SourceWindowGapIoError {
				t.Fatalf("coverage = %+v, want not observed with I/O error", got)
			}
		})
	}
}

func TestJoiningLaunchWithCanceledWaitDoesNotStartSecondBaseline(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	dir := outsideDir(t)
	window := openOutsideWindow(t, store, ctx, root)
	fault := installSnapshots(store, &faultySnapshots{})
	joins := window.window.outside.joins
	joins <- struct{}{}
	wait, cancel := context.WithCancel(ctx)
	cancel()
	err := window.AddOutsideRoots(wait, []OutsideRootSpec{{Path: dir}})
	<-joins
	if !errors.Is(err, context.Canceled) || fault.captures() != 0 {
		t.Fatalf("join = %v, captures = %d; want canceled wait before any capture", err, fault.captures())
	}
	testutil.FailErr(t, "join after the prior launch releases", window.AddOutsideRoots(ctx, []OutsideRootSpec{{Path: dir}}))
	if fault.captures() != 1 {
		t.Fatalf("captures = %d, want one baseline after admission", fault.captures())
	}
	closeAndSettle(t, ctx, window)
}
