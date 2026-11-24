package cadence

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDispatchRootRecordsClaimAndUnchangedDecisionsWhileReusingSharedSnapshot(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	ctx := t.Context()

	rawDir := t.TempDir()
	canonical, err := scanbase.CanonicalPath(rawDir)
	testutil.FailErr(t, "canonical root", err)
	testutil.FailErr(t, "seed main.go", os.WriteFile(filepath.Join(canonical, "main.go"), []byte("package main\n"), 0o600))
	testutil.FailErr(t, "seed readme.md", os.WriteFile(filepath.Join(canonical, "README.md"), []byte("docs v1\n"), 0o600))
	repochangetest.Watch(t, canonical)

	snapshots := cadence.Coordinator.SnapshotStore()
	privA := t.TempDir()
	testutil.FailErr(t, "seed privA", os.WriteFile(filepath.Join(privA, "a.go"), []byte("package a\n"), 0o600))
	repochangetest.Watch(t, privA)

	// Project A warms the shared root's snapshot before cadence runs.
	_, err = snapshots.Ensure(ctx, sourcesnapshot.Request{
		Roots: []sourcesnapshot.Root{{Path: canonical}, {Path: privA}},
	})
	testutil.FailErr(t, "warm project A snapshot", err)
	beforeBaseline := snapshots.Metrics()

	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	testutil.FailErr(t, "baseline root", cadence.BaselineRoot(ctx, canonical))
	requireSeriesBased(t, cadence, canonical)
	afterBaseline := snapshots.Metrics()
	if afterBaseline.Surveys != beforeBaseline.Surveys {
		t.Fatalf("baseline over covered root ran %d extra physical surveys", afterBaseline.Surveys-beforeBaseline.Surveys)
	}

	// Queue an unchanged notification (same file content) so dispatchRoot exercises the unchanged decision path.
	testutil.FailErr(t, "note unchanged write", noteCadenceWrites(ctx, cadence, canonical, []string{"main.go"}))
	clock.Advance(time.Minute)
	cadence.Tick(ctx)

	afterUnchanged := snapshots.Metrics()
	if afterUnchanged.Surveys != afterBaseline.Surveys {
		t.Fatalf("unchanged cadence dispatch ran %d extra surveys", afterUnchanged.Surveys-afterBaseline.Surveys)
	}
	scansAfterUnchanged, err := store.Reads.ListByCanonicalPath(ctx, canonical)
	testutil.FailErr(t, "list scans after unchanged", err)
	if len(scansAfterUnchanged) != 0 {
		t.Fatalf("unchanged dispatch enqueued %d scans, want 0", len(scansAfterUnchanged))
	}

	logs := logBuf.String()
	for _, token := range []string{"state=claimed", "claim_token=", "series_count=", "state=unchanged", "snapshot_id="} {
		if !strings.Contains(logs, token) {
			t.Fatalf("dispatch logs missing %q:\n%s", token, logs)
		}
	}

	// Genuine source edit still dispatches scanner runs.
	testutil.FailErr(t, "modify main.go", os.WriteFile(filepath.Join(canonical, "main.go"), []byte("package main\nfunc changed() {}\n"), 0o600))
	testutil.FailErr(t, "note real write", noteCadenceWrites(ctx, cadence, canonical, []string{"main.go"}))
	clock.Advance(time.Minute)
	cadence.Tick(ctx)

	scansAfterChange, err := store.Reads.ListByCanonicalPath(ctx, canonical)
	testutil.FailErr(t, "list scans after change", err)
	if len(scansAfterChange) == 0 {
		t.Fatal("genuine source change did not enqueue scanner runs")
	}
}
