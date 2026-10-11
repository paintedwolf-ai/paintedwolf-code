package repoinfo

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func newProviderWithAnalyze(fn func(ctx context.Context, projectDir string) (*Brief, error)) *fileProvider {
	ctx, cancel := context.WithCancel(context.Background())
	return &fileProvider{
		cache:     make(map[string]*Brief),
		ctx:       ctx,
		cancel:    cancel,
		analyzeFn: fn,
	}
}

// Missing measurements leave emptiness unknown.
func TestKnownEmptyIsUnknownWithoutASource(t *testing.T) {
	p := newProviderWithAnalyze(nil)
	defer func() { _ = p.Close() }()
	got, err := p.KnownEmpty(t.Context(), t.TempDir())
	testutil.FailErr(t, "measure without a catalog", err)
	if got {
		t.Fatal("an unmeasured tree reported empty")
	}
}

func TestProgressiveBriefReturnsBeforeSlowAnalyze(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))

	release := make(chan struct{})
	var started atomic.Int32
	p := newProviderWithAnalyze(func(ctx context.Context, projectDir string) (*Brief, error) {
		started.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &Brief{FileCount: 1, Languages: []string{"Go"}, GeneratedAt: time.Now().UTC()}, nil
	})
	defer func() { _ = p.Close() }()

	type briefResult struct {
		b   *Brief
		err error
	}
	done := make(chan briefResult, 1)
	go func() {
		b, err := p.Brief(context.Background(), dir)
		done <- briefResult{b, err}
	}()

	var brief *Brief
	select {
	case res := <-done:
		testutil.FailErr(t, "Brief", res.err)
		brief = res.b
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Brief blocked waiting for slow Analyze")
	}
	// A cold root returns an unmeasured brief at once, carrying no count.
	if brief.Materialized || brief.FileCount != 0 {
		t.Fatalf("cold brief = %+v, want an unmeasured placeholder", brief)
	}
	testutil.WaitFor(t, 2*time.Second, func() bool { return started.Load() > 0 })
	if starts := started.Load(); starts != 1 {
		t.Fatalf("analyze starts while blocked = %d want 1", starts)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := AwaitBrief(ctx, p, dir)
	testutil.FailErr(t, "AwaitBrief", err)
	if !got.Materialized {
		t.Fatal("want materialized after Analyze")
	}
}

func TestProgressiveBriefSingleflightConcurrent(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))

	var calls atomic.Int32
	block := make(chan struct{})
	p := newProviderWithAnalyze(func(ctx context.Context, projectDir string) (*Brief, error) {
		calls.Add(1)
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &Brief{FileCount: 1, GeneratedAt: time.Now().UTC()}, nil
	})
	defer func() { _ = p.Close() }()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Brief(context.Background(), dir)
			if err != nil {
				t.Errorf("Brief: %v", err)
			}
			p.Warm(dir)
		}()
	}
	// Wait until every caller joins the singleflight.
	wg.Wait()
	close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := AwaitBrief(ctx, p, dir)
	testutil.FailErr(t, "AwaitBrief", err)
	if calls.Load() != 1 {
		t.Fatalf("analyze calls = %d want 1 (singleflight)", calls.Load())
	}
}

func TestProgressiveBriefServesMaterializedResultDuringRefresh(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write first source", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var calls atomic.Int32
	p := newProviderWithAnalyze(func(ctx context.Context, projectDir string) (*Brief, error) {
		call := calls.Add(1)
		if call == 2 {
			close(refreshStarted)
			select {
			case <-releaseRefresh:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &Brief{FileCount: int(call), GeneratedAt: time.Now().UTC()}, nil
	})
	defer func() { _ = p.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	first, err := AwaitBrief(ctx, p, dir)
	testutil.FailErr(t, "materialize first brief", err)
	if first.FileCount != 1 {
		t.Fatalf("first file count = %d, want 1", first.FileCount)
	}

	// A catalog refresh retains the materialized summary after its refractory.
	first.GeneratedAt = time.Now().Add(-briefRevalidateAfter)
	p.store(dir, first)
	p.ensureAnalyze(dir)
	for {
		select {
		case <-refreshStarted:
			goto refreshRunning
		case <-ctx.Done():
			t.Fatal("refresh did not start")
		default:
			_, err = p.Brief(ctx, dir)
			testutil.FailErr(t, "request stale brief refresh", err)
			time.Sleep(time.Millisecond)
		}
	}

refreshRunning:
	stale, err := p.Brief(ctx, dir)
	testutil.FailErr(t, "read brief during refresh", err)
	if !stale.Materialized || stale.FileCount != 1 {
		t.Fatalf("brief during refresh = %+v, want prior materialized result", stale)
	}
	close(releaseRefresh)
	for {
		refreshed, briefErr := p.Brief(ctx, dir)
		testutil.FailErr(t, "read refreshed brief", briefErr)
		if refreshed.Materialized && refreshed.FileCount == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("refreshed brief was not published")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestProgressiveBriefPublishesConcurrentObservationAsStale(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	p := newProviderWithAnalyze(func(ctx context.Context, projectDir string) (*Brief, error) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &Brief{FileCount: 0, GeneratedAt: time.Now().UTC()}, nil
	})
	defer func() { _ = p.Close() }()

	_, err := p.Brief(context.Background(), dir)
	testutil.FailErr(t, "start brief", err)
	<-started
	repochange.Advance(dir)
	close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var observed *Brief
	for observed == nil || !observed.Materialized {
		observed, err = p.Brief(ctx, dir)
		testutil.FailErr(t, "read concurrent observation", err)
		if observed.Materialized {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("concurrent observation was discarded")
		case <-time.After(time.Millisecond):
		}
	}
	if observed.FileCount != 0 || !observed.Materialized {
		t.Fatalf("concurrent observation = %+v", observed)
	}
}

func TestAnalyzeRootsDoesNotAwaitFullWalk(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))

	release := make(chan struct{})
	started := make(chan struct{})
	p := newProviderWithAnalyze(func(ctx context.Context, projectDir string) (*Brief, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &Brief{FileCount: 99, GeneratedAt: time.Now().UTC()}, nil
	})
	defer func() { _ = p.Close() }()

	type mrbResult struct {
		m   *MultiRootBrief
		err error
	}
	done := make(chan mrbResult, 1)
	go func() {
		m, err := AnalyzeRoots(context.Background(), []projectroot.RootRef{{
			Path: dir, IsPrimary: true,
		}}, DefaultBriefBudget(), p)
		done <- mrbResult{m, err}
	}()
	var mrb *MultiRootBrief
	select {
	case res := <-done:
		testutil.FailErr(t, "AnalyzeRoots", res.err)
		mrb = res.m
	case <-time.After(500 * time.Millisecond):
		t.Fatal("AnalyzeRoots blocked on full Analyze")
	}
	if mrb.PrimaryMaterialized() {
		t.Fatal("want progressive AnalyzeRoots (not materialized)")
	}
	// The placeholder carries no count.
	if mrb.Roots[0].Brief.FileCount != 0 {
		t.Fatalf("placeholder file count = %d, want none", mrb.Roots[0].Brief.FileCount)
	}
	if got := mrb.PrimaryRepoBrief(); got.FileCount != 0 || !got.GeneratedAt.IsZero() {
		t.Fatalf("agent-facing brief exposed the unmeasured placeholder: %+v", got)
	}
	if roots := mrb.OrientationRoots(); len(roots) != 0 {
		t.Fatalf("agent-facing roots exposed the unmeasured placeholder: %+v", roots)
	}
	close(release)
}

func TestWarmDoesNotBlockOnSlowAnalyze(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	release := make(chan struct{})
	started := make(chan struct{})
	p := newProviderWithAnalyze(func(ctx context.Context, projectDir string) (*Brief, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &Brief{FileCount: 1, GeneratedAt: time.Now().UTC()}, nil
	})
	defer func() { _ = p.Close() }()

	returned := make(chan struct{})
	go func() {
		p.Warm(dir)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Warm waited for analysis")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Warm did not start analysis")
	}
	close(release)
}

// Continuous writes defer remeasurement until discovery settles.
func TestBriefPacingIgnoresChurnAndCatchesPartialCoverage(t *testing.T) {
	now := time.Now()
	fresh := func(gen uint64, partial bool) *Brief {
		return &Brief{Materialized: true, Generation: gen, Partial: partial, GeneratedAt: now}
	}
	for _, tc := range []struct {
		name  string
		brief *Brief
		files indexGeneration
		at    time.Time
		want  bool
	}{
		{"no brief at all", nil, indexGeneration{generation: 7, complete: true}, now, true},
		{"nothing published yet", fresh(0, false), indexGeneration{}, now, false},
		{"same generation", fresh(7, false), indexGeneration{generation: 7, complete: true}, now, false},
		{"same generation with settled omissions", fresh(7, true), indexGeneration{generation: 7, complete: true}, now, false},
		{
			name:  "refresh settled without another generation",
			brief: &Brief{Materialized: true, Generation: 7, Partial: true, Refreshing: true, GeneratedAt: now},
			files: indexGeneration{generation: 7, complete: true}, at: now, want: false,
		},
		{
			// Ongoing discovery defers remeasurement regardless of generation age.
			name: "newer generation, discovery still in flight", brief: fresh(7, false),
			files: indexGeneration{generation: 999}, at: now.Add(time.Hour), want: false,
		},
		{
			name: "newer settled generation, rate limited", brief: fresh(7, false),
			files: indexGeneration{generation: 999, complete: true},
			at:    now.Add(briefRevalidateAfter - time.Second), want: false,
		},
		{
			name: "newer settled generation, past the limit", brief: fresh(7, false),
			files: indexGeneration{generation: 999, complete: true},
			at:    now.Add(briefRevalidateAfter), want: true,
		},
		{
			// Completed discovery retains the measurement refractory.
			name: "partial brief, index now covers the tree", brief: fresh(7, true),
			files: indexGeneration{generation: 8, complete: true}, at: now, want: false,
		},
		{
			// Background refinement owns pending discovery.
			name: "partial brief, index still discovering", brief: fresh(7, true),
			files: indexGeneration{generation: 8}, at: now, want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := briefStale(tc.brief, tc.files, tc.at); got != tc.want {
				t.Fatalf("briefStale = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBriefPacingScalesWithMeasurementCost(t *testing.T) {
	now := time.Now()
	brief := &Brief{Materialized: true, Generation: 1, Refreshing: true, GeneratedAt: now, measurementDuration: 12 * time.Second}
	files := indexGeneration{generation: 2, complete: true}
	if briefStale(brief, files, now.Add(time.Minute)) {
		t.Fatal("expensive brief bypassed its cost refractory")
	}
	if !briefStale(brief, files, now.Add(2*time.Minute)) {
		t.Fatal("completed newer index did not remeasure after refractory")
	}
}

type refinementCatalog struct {
	repositoryIndex
	status sourcecatalog.TreeStatus
}

func (c refinementCatalog) IndexStatus(context.Context, string, sourcecatalog.Root) (sourcecatalog.TreeStatus, error) {
	return c.status, nil
}

func TestRefinementConvergesWithoutRemeasuringSameGeneration(t *testing.T) {
	p := newProviderWithAnalyze(nil)
	defer func() { _ = p.Close() }()
	p.resolveCatalogRoot = func(context.Context, string) (CatalogRoot, bool, error) {
		return CatalogRoot{ProjectID: "p", RootID: "r"}, true, nil
	}
	p.catalog = refinementCatalog{status: sourcecatalog.TreeStatus{Revision: 7, Complete: true}}
	brief := &Brief{Materialized: true, Generation: 7, Refreshing: true, Partial: true, GeneratedAt: time.Now()}
	p.store("root", brief)
	_, refine := p.awaitRefinement("root", brief, briefDiscoveryPoll, time.Now().Add(time.Minute))
	if refine {
		t.Fatal("unchanged completed generation requested a full measurement")
	}
	settled := p.load("root")
	if settled.Refreshing || settled.Partial {
		t.Fatal("settled coverage was not projected onto the measured brief")
	}
}

func TestWarmCannotBypassMeasurementRefractory(t *testing.T) {
	var calls atomic.Int32
	p := newProviderWithAnalyze(func(context.Context, string) (*Brief, error) {
		calls.Add(1)
		return &Brief{}, nil
	})
	root := t.TempDir()
	p.store(root, &Brief{Materialized: true, Partial: true, GeneratedAt: time.Now(), measurementDuration: time.Minute})
	p.Warm(root)
	testutil.FailErr(t, "close paced provider", p.Close())
	if calls.Load() != 0 {
		t.Fatal("warm bypassed measurement pacing")
	}
}
