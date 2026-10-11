package cadence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCadenceWriteBurstCoalescesAndWaitsForIdle(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedSizedProject(t, dir, 40)
	testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	testutil.FailErr(t, "write a.go", os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package a\n"), 0o644))
	testutil.FailErr(t, "write b.go", os.WriteFile(filepath.Join(dir, "src", "b.go"), []byte("package b\n"), 0o644))
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	completeCadenceScans(t, cadence, store)

	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	before, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "list before burst", err)

	testutil.FailErr(t, "change a.go", os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package a\nvar A = 1\n"), 0o644))
	testutil.FailErr(t, "NoteWrites a.go", noteCadenceWrites(context.Background(), cadence, dir, []string{"src/a.go"}))
	clock.Advance(2 * time.Second)
	testutil.FailErr(t, "change b.go", os.WriteFile(filepath.Join(dir, "src", "b.go"), []byte("package b\nvar B = 1\n"), 0o644))
	testutil.FailErr(t, "NoteWrites b.go", noteCadenceWrites(context.Background(), cadence, dir, []string{"src/b.go"}))
	clock.Advance(2 * time.Second)
	cadence.Tick(context.Background())
	afterEarly, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "list early burst", err)
	if len(afterEarly) != len(before) {
		t.Fatalf("burst fired before idle: %d -> %d", len(before), len(afterEarly))
	}

	clock.Advance(3 * time.Second)
	cadence.Tick(context.Background())
	after, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "list after burst", err)
	var bursts []api.CodeScan
	for _, sc := range after {
		if sc.Trigger == api.ScanTriggerWriteBurst {
			bursts = append(bursts, sc)
		}
	}
	// Each scanner receives the settled source delta.
	if len(bursts) != 3 {
		t.Fatalf("write_burst rows = %d want one per scanner: %+v", len(bursts), bursts)
	}
	scanners := map[string]bool{}
	for _, burst := range bursts {
		scanners[burst.ScannerID] = true
		if burst.TargetKind != api.ScanTargetPaths {
			t.Fatalf("write_burst %s target = %q, want the delta", burst.ScannerID, burst.TargetKind)
		}
	}
	if !scanners["lycaon-sast"] || !scanners["lycaon-secrets"] || !scanners["lycaon-sca"] {
		t.Fatalf("write_burst scanners = %v", scanners)
	}
}

func TestCadenceWriteBurstSkipsPathsExcludedFromSourceSnapshots(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedSizedProject(t, dir, 40)
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	completeCadenceScans(t, cadence, store)

	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	before, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "list before excluded write", err)

	excluded := filepath.Join(settingsoverlay.DirName(), "blueprints", "plan.md")
	testutil.FailErr(t, "NoteWrites excluded blueprint", noteCadenceWrites(context.Background(), cadence, dir, []string{excluded}))
	clock.Advance(5 * time.Second)
	cadence.Tick(context.Background())

	after, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "list after excluded write", err)
	if len(after) != len(before) {
		t.Fatalf("excluded blueprint scheduled a write-burst scan: %d -> %d", len(before), len(after))
	}
}

func TestCadenceConcurrentWriteBurstsDoNotLosePaths(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedSizedProject(t, dir, 80)
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	completeCadenceScans(t, cadence, store)

	const changes = 12
	errCh := make(chan error, changes)
	var wg sync.WaitGroup
	for i := 0; i < changes; i++ {
		name := fmt.Sprintf("changed-%02d.go", i)
		testutil.FailErr(t, "write changed file", os.WriteFile(filepath.Join(dir, name), []byte("package changed\n"), 0o644))
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- noteCadenceWrites(context.Background(), cadence, dir, []string{name})
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		testutil.FailErr(t, "concurrent NoteWrites", err)
	}

	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	series, err := store.GetSeries(context.Background(), canonical, "lycaon-sast")
	testutil.FailErr(t, "GetSeries SAST", err)
	if series == nil || len(series.DesiredPaths) != changes {
		t.Fatalf("coalesced paths = %#v want %d paths", series, changes)
	}
}

func TestCadenceWriteBurstScansWithoutAFullPass(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "NoteWrites", noteCadenceWrites(context.Background(), cadence, dir, []string{"main.go"}))
	clock.Advance(5 * time.Second)
	cadence.Tick(context.Background())

	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(scans) != 3 {
		t.Fatalf("a change without a full pass behind it still scans as a delta; got %d scans", len(scans))
	}
	completeCadenceScans(t, cadence, store)
	for _, sc := range scans {
		completed, err := store.Get(context.Background(), sc.ID)
		testutil.FailErr(t, "read completed delta", err)
		if completed.CoverageStatus != api.ScanCoveragePartial {
			t.Fatalf("delta without a full pass reports %q coverage, want partial", completed.CoverageStatus)
		}
	}
}

func TestCadenceContinuousWritesCappedAtMaxDefer(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "NoteWrites first", noteCadenceWrites(context.Background(), cadence, dir, []string{"main.go"}))
	capDeadline := clock.Now().Add(cadence.cadenceCfg().MaxDefer())

	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	// The maximum wait makes continuous writes eligible for capture.
	step := cadence.cadenceCfg().WriteBurstSettle() / 2
	for clock.Now().Add(step).Before(capDeadline) {
		clock.Advance(step)
		testutil.FailErr(t, "NoteWrites loop", noteCadenceWrites(context.Background(), cadence, dir, []string{"main.go"}))
		cadence.Tick(context.Background())
		scans, listErr := store.ListByCanonicalPath(context.Background(), canonical)
		testutil.FailErr(t, "ListByCanonicalPath during writes", listErr)
		if len(scans) != 0 {
			t.Fatalf("delta fired before max defer at %v (cap %v)", clock.Now(), capDeadline)
		}
	}

	clock.Advance(step + time.Second)
	testutil.FailErr(t, "NoteWrites past cap", noteCadenceWrites(context.Background(), cadence, dir, []string{"main.go"}))
	cadence.Tick(context.Background())
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath past cap", err)
	if len(scans) != 3 {
		t.Fatalf("capped delta len = %d want 3", len(scans))
	}
	series, err := store.ListSeriesForPath(context.Background(), canonical)
	testutil.FailErr(t, "ListSeriesForPath", err)
	for _, row := range series {
		if !row.MaxDueAt.IsZero() {
			t.Fatalf("scanner %q cap not cleared: %v", row.ScannerID, row.MaxDueAt)
		}
	}
}

type drainingRepochangeScope struct {
	entered  chan struct{}
	canceled chan struct{}
	allow    chan struct{}
}

func (s *drainingRepochangeScope) Capture(ctx context.Context, root string) *sourcescope.Scope {
	close(s.entered)
	<-ctx.Done()
	close(s.canceled)
	<-s.allow
	return sourcescope.New(root, sourcescope.Options{})
}

func TestCadenceRepochangeReleaseCancelsAndDrainsCopiedWork(t *testing.T) {
	cadence, _ := newTestCadence(t, newTestClock())
	scope := &drainingRepochangeScope{entered: make(chan struct{}), canceled: make(chan struct{}), allow: make(chan struct{})}
	cadence.Scopes = scope
	release := cadence.ObserveRepochange()
	var allow sync.Once
	t.Cleanup(func() {
		allow.Do(func() { close(scope.allow) })
		testutil.FailErr(t, "release cadence observer", release(context.Background()))
	})
	event := repochange.Event{ProjectDir: t.TempDir(), Kind: repochange.WorktreeChanged, Paths: []string{"changed.go"}}
	done := make(chan struct{})
	go func() { defer close(done); repochange.Notify(t.Context(), event) }()
	select {
	case <-scope.entered:
	case <-t.Context().Done():
		t.Fatal("notification did not enter capture")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := release(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("active observer drain = %v, want cancellation", err)
	}
	select {
	case <-scope.canceled:
	case <-t.Context().Done():
		t.Fatal("release did not cancel capture")
	}
	// A callback copied after sealing must not start another scope capture.
	repochange.Notify(t.Context(), event)
	allow.Do(func() { close(scope.allow) })
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("notification did not finish")
	}
	testutil.FailErr(t, "retry observer drain", release(t.Context()))
	testutil.FailErr(t, "repeat observer drain", release(t.Context()))
	repochange.Notify(t.Context(), event)
	testutil.FailErr(t, "nil cadence observer", (*Service)(nil).ObserveRepochange()(t.Context()))
}
