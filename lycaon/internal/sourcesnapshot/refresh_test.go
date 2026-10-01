package sourcesnapshot

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRefreshDemandFollowsWatcherWhileCurrencyChecksDisk(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "main.go", "package first\n")
	base, err := store.EnsurePath(t.Context(), root, VerifyStat)
	testutil.FailErr(t, "publish base", err)
	writeSource(t, root, "main.go", "package changed\n")
	refresh, err := store.RefreshNeeded(t.Context(), base.ID)
	testutil.FailErr(t, "read observed demand", err)
	if refresh {
		t.Fatal("scheduling walked live files before their event arrived")
	}
	current, err := store.IsCurrent(t.Context(), base.ID)
	testutil.FailErr(t, "verify live bytes", err)
	if current {
		t.Fatal("strict verification trusted an undelivered event")
	}
	notifyChange(root, "main.go")
	refresh, err = store.RefreshNeeded(t.Context(), base.ID)
	testutil.FailErr(t, "read delivered demand", err)
	if !refresh {
		t.Fatal("delivered change did not demand a successor")
	}
	next, err := store.EnsurePath(t.Context(), root, VerifyStat)
	testutil.FailErr(t, "publish successor", err)
	refresh, err = store.RefreshNeeded(t.Context(), next.ID)
	testutil.FailErr(t, "read settled demand", err)
	if refresh {
		t.Fatal("settled generation still demanded a successor")
	}
}

func TestRefreshDemandVerifiesUncoveredRoot(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "main.go", "package first\n")
	base, err := store.EnsurePath(t.Context(), root, VerifyStat)
	testutil.FailErr(t, "publish base", err)
	repochange.ResetWatchersForTest()
	writeSource(t, root, "main.go", "package changed\n")
	refresh, err := store.RefreshNeeded(t.Context(), base.ID)
	testutil.FailErr(t, "read uncovered demand", err)
	if !refresh {
		t.Fatal("uncovered change was lost")
	}
}

func TestRefreshDemandIgnoresExcludedChurnAndRebuildsChangedScope(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	store.SetScopes(scopeProvider{plane: sourcescope.Plane{IgnoreFiles: true}})
	writeSource(t, root, ".gitignore", "ignored.go\n")
	writeSource(t, root, "main.go", "package main\n")
	writeSource(t, root, "ignored.go", "package ignored\n")
	base, err := store.EnsurePath(t.Context(), root, VerifyStat)
	testutil.FailErr(t, "publish base", err)
	notifyChange(root, "ignored.go")
	refresh, err := store.RefreshNeeded(t.Context(), base.ID)
	testutil.FailErr(t, "read excluded demand", err)
	if refresh {
		t.Fatal("excluded churn demanded a successor")
	}
	store.SetScopes(scopeProvider{plane: sourcescope.Plane{IgnoreFiles: false}})
	refresh, err = store.RefreshNeeded(t.Context(), base.ID)
	testutil.FailErr(t, "read changed scope", err)
	if !refresh {
		t.Fatal("changed scope reused the previous admission")
	}
	next, err := store.EnsurePath(t.Context(), root, VerifyStat)
	testutil.FailErr(t, "publish changed scope", err)
	if _, found := entryPaths(t, store, next)["ignored.go"]; !found {
		t.Fatal("newly admitted file was lost by incremental publication")
	}
}

func TestPublicationPreservesResyncReceivedAfterItsPlan(t *testing.T) {
	tracker := newDeltaTracker()
	root := t.TempDir()
	planSequence := tracker.sequence()
	tracker.observe(context.Background(), repochange.Event{
		ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{"../outside"},
	})
	tracker.coveredBy(root, "snapshot", "scope", planSequence, true)
	if !tracker.moved(root, deltaPlan{upTo: planSequence}) {
		t.Fatal("publication erased a later resync")
	}
}

func TestRefreshPendingDoesNotWalkUncoveredRoots(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "main.go", "package main\n")
	base, err := store.EnsurePath(t.Context(), root, VerifyStat)
	testutil.FailErr(t, "publish base", err)
	repochange.ResetWatchersForTest()
	store.onSurvey = func(string) { t.Error("terminal freshness check walked the repository") }
	store.onCapture = func(string) { t.Error("terminal freshness check read source bytes") }
	pending, err := store.RefreshPending(t.Context(), base.ID)
	testutil.FailErr(t, "check recorded freshness", err)
	if !pending {
		t.Fatal("uncovered root was certified fresh")
	}
}
