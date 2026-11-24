package sourcesnapshot

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMilestoneSharedPhysicalBaseline(t *testing.T) {
	store := openSnapshotStore(t)
	shared, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	for _, r := range []string{shared, a, b} {
		coverRoot(t, r)
		writeSource(t, r, "main.go", "package a\n")
	}
	var sharedSurveys int
	store.onSurvey = func(r string) {
		if r == shared {
			sharedSurveys++
		}
	}
	steps := []struct {
		label string
		roots []Root
		want  int
	}{
		{"project A", []Root{{Path: shared}, {Path: a}}, 1},
		{"project B", []Root{{Path: shared}, {Path: b}}, 0},
		{"scanner", []Root{{Path: shared}}, 0},
		{"repeat A", []Root{{Path: shared}, {Path: a}}, 0},
		{"scope change", []Root{{Path: shared}}, 1},
		{"after scope change", []Root{{Path: shared}, {Path: b}}, 0},
		{"after rescope repeat", []Root{{Path: shared}, {Path: a}}, 0},
	}
	for _, step := range steps {
		before := sharedSurveys
		if step.label == "scope change" {
			writeSource(t, shared, ".gitignore", "main.go\n")
			notifyChange(shared, ".gitignore")
		}
		snap, err := store.Ensure(t.Context(), Request{Roots: step.roots})
		testutil.FailErr(t, step.label, err)
		if got := sharedSurveys - before; got != step.want {
			t.Fatalf("%s: shared surveys = %d, want %d", step.label, got, step.want)
		}
		if step.label == "scope change" {
			if _, ok := entryPaths(t, store, snap)["main.go"]; ok {
				t.Fatal("ignored file still present")
			}
		}
	}
}

func TestMilestoneScopeControl(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "public.go", "package a\n")
	writeSource(t, root, "hidden/a.go", "package b\n")
	for _, floor := range [][]string{nil, {"hidden"}, nil} {
		scope := sourcescope.New(root, sourcescope.Options{Floor: floor})
		snap, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}, Scope: scope})
		testutil.FailErr(t, "ensure scoped snapshot", err)
		_, visible := entryPaths(t, store, snap)["hidden/a.go"]
		if visible != (len(floor) == 0) {
			t.Fatalf("floor=%v visible=%v", floor, visible)
		}
	}
}

func TestMilestoneConcurrentRescope(t *testing.T) {
	store := openSnapshotStore(t)
	shared, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	for _, r := range []string{shared, a, b} {
		coverRoot(t, r)
		writeSource(t, r, "keep.go", "package keep\n")
	}
	request := func(roots ...string) Request {
		req := Request{}
		for _, r := range roots {
			req.Roots = append(req.Roots, Root{Path: r})
		}
		return req
	}
	_, err := store.Ensure(t.Context(), request(shared))
	testutil.FailErr(t, "initial shared ensure", err)

	writeSource(t, shared, ".gitignore", "ignored.go\n")
	notifyChange(shared, ".gitignore")

	started, release := make(chan struct{}), make(chan struct{})
	var surveys atomic.Int32
	store.onSurvey = func(root string) {
		if root == shared && surveys.Add(1) == 1 {
			close(started)
			<-release
		}
	}
	type result struct {
		roots []string
		snap  Snapshot
		err   error
	}
	results := make(chan result, 3)
	run := func(roots ...string) {
		s, e := store.Ensure(t.Context(), request(roots...))
		results <- result{roots: roots, snap: s, err: e}
	}
	go run(shared)
	<-started
	var launched sync.WaitGroup
	launched.Add(2)
	for _, private := range []string{a, b} {
		go func(priv string) {
			launched.Done()
			run(shared, priv)
		}(private)
	}
	launched.Wait()
	duringBody := "package keep\nconst v = 2\n"
	writeSource(t, shared, "during.go", duringBody)
	notifyChange(shared, "during.go")
	close(release)

	wantContentID := "git:" + sourceblob.ContentGitOIDs([]byte(duringBody)).SHA1
	for range 3 {
		r := <-results
		testutil.FailErr(t, "concurrent ensure", r.err)
		entry := lookupRootEntry(t, store, r.snap, shared, "during.go")
		if entry.ContentID() != wantContentID {
			t.Fatalf("during.go content id = %q, want %q", entry.ContentID(), wantContentID)
		}
		for _, priv := range r.roots[1:] {
			present, err := store.HasPath(t.Context(), r.snap.ID, priv, "keep.go")
			testutil.FailErr(t, "check private root entry", err)
			if !present {
				t.Fatalf("private root %s missing keep.go", priv)
			}
		}
	}
	if surveys.Load() != 1 {
		t.Fatalf("shared surveys = %d, want 1", surveys.Load())
	}
}

func TestDuplicateDemandDuringRepairIncompleteWatcherAndVerifyContent(t *testing.T) {
	store := openSnapshotStore(t)
	var eventsMu sync.Mutex
	var events []SurveyEvent
	stopObserve := store.ObserveSurveys(func(ev SurveyEvent) {
		eventsMu.Lock()
		events = append(events, ev)
		eventsMu.Unlock()
	})
	defer stopObserve()

	unwatched := t.TempDir()
	writeSource(t, unwatched, "alpha.go", "package alpha\n")

	before := store.Metrics()
	snap1, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: unwatched}}})
	testutil.FailErr(t, "unwatched first ensure", err)
	after1 := store.Metrics()
	if after1.ConfirmRuns <= before.ConfirmRuns {
		t.Fatalf("incomplete watcher skipped confirmation: before=%+v after=%+v", before, after1)
	}

	// Equal snapshot IDs without watcher coverage still require survey + confirmation.
	snap2, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: unwatched}}})
	testutil.FailErr(t, "unwatched second ensure", err)
	if snap2.ID != snap1.ID {
		t.Fatalf("snapshot ID changed on unchanged tree: %s vs %s", snap1.ID, snap2.ID)
	}
	after2 := store.Metrics()
	if after2.Surveys <= after1.Surveys || after2.ConfirmRuns <= after1.ConfirmRuns {
		t.Fatalf("incomplete watcher reused covered head without reconfirmation: %+v -> %+v", after1, after2)
	}

	// Under complete coverage, VerifyMetadata reuses covered manifest (0 surveys, 0 content reads),
	// while explicit VerifyContent forces content rereading.
	watched := t.TempDir()
	coverRoot(t, watched)
	writeSource(t, watched, "verified.go", "package verified\n")
	snapMeta, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: watched}}})
	testutil.FailErr(t, "watched initial ensure", err)

	mBeforeMeta := store.Metrics()
	snapMeta2, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: watched}}})
	testutil.FailErr(t, "watched metadata repeat", err)
	mAfterMeta := store.Metrics()
	if snapMeta2.ID != snapMeta.ID || mAfterMeta.Surveys != mBeforeMeta.Surveys || mAfterMeta.ContentReads != mBeforeMeta.ContentReads {
		t.Fatalf("watched repeat did physical work: %+v -> %+v", mBeforeMeta, mAfterMeta)
	}

	snapContent, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: watched}}, Verify: VerifyContent})
	testutil.FailErr(t, "watched explicit VerifyContent", err)
	mAfterContent := store.Metrics()
	if snapContent.ID != snapMeta.ID {
		t.Fatalf("VerifyContent changed unchanged snapshot ID: %s vs %s", snapContent.ID, snapMeta.ID)
	}
	if mAfterContent.Surveys <= mAfterMeta.Surveys || mAfterContent.ContentReads <= mAfterMeta.ContentReads {
		t.Fatalf("VerifyContent skipped survey or content reread: %+v -> %+v", mAfterMeta, mAfterContent)
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()
	var reasons []SurveyReason
	var phases []SurveyPhase
	for _, ev := range events {
		reasons = append(reasons, ev.Reason)
		phases = append(phases, ev.Phase)
	}
	for _, want := range []SurveyReason{SurveyIncompleteWatcher, SurveyNoCoveredHead, SurveyExplicitVerifyContent} {
		if !slices.Contains(reasons, want) {
			t.Fatalf("observed reasons %v missing %q", reasons, want)
		}
	}
	for _, want := range []SurveyPhase{SurveyPhaseInitial, SurveyPhaseConfirm} {
		if !slices.Contains(phases, want) {
			t.Fatalf("observed phases %v missing %q", phases, want)
		}
	}
}

func TestMultiScopeSeparationBothOrderingsAndPostWaitFreshness(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "public.go", "package pub\n")
	writeSource(t, root, "hidden/secret.go", "package secret\n")

	fullScope := sourcescope.New(root, sourcescope.Options{})
	narrowScope := sourcescope.New(root, sourcescope.Options{Floor: []string{"hidden"}})

	coldStore := openSnapshotStore(t)
	coldFull, err := coldStore.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}, Scope: fullScope})
	testutil.FailErr(t, "cold full reference", err)
	coldNarrow, err := coldStore.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}, Scope: narrowScope})
	testutil.FailErr(t, "cold narrow reference", err)

	// Exercise both orderings: full->narrow->full->narrow and narrow->full->narrow->full.
	orderings := [][]*sourcescope.Scope{
		{fullScope, narrowScope, fullScope, narrowScope},
		{narrowScope, fullScope, narrowScope, fullScope},
	}
	for idx, seq := range orderings {
		s := openSnapshotStore(t)
		var surveys int
		s.onSurvey = func(r string) {
			if r == root {
				surveys++
			}
		}
		for step, sc := range seq {
			before := surveys
			snap, err := s.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}, Scope: sc})
			testutil.FailErr(t, "scoped ensure", err)
			wantID := coldFull.ID
			if sc == narrowScope {
				wantID = coldNarrow.ID
			}
			if snap.ID != wantID {
				t.Fatalf("ordering %d step %d: snap ID = %s, want %s", idx, step, snap.ID, wantID)
			}
			// First two distinct scopes each survey once; subsequent switches reuse their retained scoped manifests!
			wantSurveys := 0
			if step < 2 {
				wantSurveys = 1
			}
			if got := surveys - before; got != wantSurveys {
				t.Fatalf("ordering %d step %d: surveys = %d, want %d", idx, step, got, wantSurveys)
			}
			_, hasHidden := entryPaths(t, s, snap)["hidden/secret.go"]
			if sc == narrowScope && hasHidden {
				t.Fatal("revoked/excluded path leaked into narrow snapshot")
			}
			if sc == fullScope && !hasHidden {
				t.Fatal("full snapshot omitted hidden/secret.go")
			}
		}
	}

	// Concurrent Ensure calls with different scopes on the same physical root never cross-contaminate job results.
	started, release := make(chan struct{}), make(chan struct{})
	var firstOnce sync.Once
	store.onSurvey = func(r string) {
		if r == root {
			firstOnce.Do(func() {
				close(started)
				<-release
			})
		}
	}
	var fullSnap, narrowSnap Snapshot
	var fullErr, narrowErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		fullSnap, fullErr = store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}, Scope: fullScope})
	}()
	<-started
	go func() {
		defer wg.Done()
		narrowSnap, narrowErr = store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}, Scope: narrowScope})
	}()
	// Deliver an edit while blocked so post-wait freshness recheck is exercised too.
	writeSource(t, root, "public2.go", "package pub\n")
	repochange.Notify(t.Context(), repochange.Event{
		ProjectDir: root,
		Kind:       repochange.WorktreeChanged,
		Paths:      []string{"public2.go"},
		Source:     repochange.SourceMutation,
	})
	close(release)
	wg.Wait()
	testutil.FailErr(t, "concurrent full scope", fullErr)
	testutil.FailErr(t, "concurrent narrow scope", narrowErr)
	if _, leaked := entryPaths(t, store, narrowSnap)["hidden/secret.go"]; leaked {
		t.Fatal("concurrent job table shared unrestricted entries into narrow scope")
	}
	if _, ok := entryPaths(t, store, fullSnap)["hidden/secret.go"]; !ok {
		t.Fatal("concurrent full scope lost hidden/secret.go")
	}
	for _, snap := range []Snapshot{fullSnap, narrowSnap} {
		if _, ok := entryPaths(t, store, snap)["public2.go"]; !ok {
			t.Fatal("post-wait freshness missed overlapping public2.go edit")
		}
	}
}

func lookupRootEntry(t *testing.T, store *Store, snapshot Snapshot, rootPath, relPath string) Entry {
	t.Helper()
	found, ok, err := store.Lookup(t.Context(), snapshot.ID, rootPath, relPath)
	testutil.FailErr(t, "lookup entry", err)
	if !ok {
		t.Fatalf("snapshot %s missing %s:%s", snapshot.ID, rootPath, relPath)
	}
	return found
}
