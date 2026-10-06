package board

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/standingpatterns"
)

func TestStandingFlagCacheKeysByEpochAndRules(t *testing.T) {
	repochange.ResetObserversForTest()
	repochange.ResetWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	dir := t.TempDir()
	repochange.MarkCoverageCompleteForTest(dir)
	var loads atomic.Int32
	cache := newStandingFlagCache(func(_ context.Context, _ string, rules []standingpatterns.Pattern) []standingpatterns.FlagCount {
		loads.Add(1)
		return []standingpatterns.FlagCount{{Label: rules[0].Label, Count: 1}}
	})
	rules := []standingpatterns.Pattern{{ID: "one", Label: "First", Pattern: "x"}}

	cache.get(t.Context(), dir, rules)
	cache.get(t.Context(), dir, rules)
	if got := loads.Load(); got != 1 {
		t.Fatalf("loads = %d, want 1 for a stable epoch", got)
	}

	repochange.Advance(dir)
	cache.get(t.Context(), dir, rules)
	if got := loads.Load(); got != 2 {
		t.Fatalf("loads = %d, want epoch invalidation", got)
	}

	changed := []standingpatterns.Pattern{{ID: "one", Label: "Changed", Pattern: "x"}}
	cache.get(t.Context(), dir, changed)
	if got := loads.Load(); got != 3 {
		t.Fatalf("loads = %d, want rules invalidation", got)
	}
}

func TestStandingFlagCacheBypassesIncompleteWatchCoverage(t *testing.T) {
	repochange.ResetObserversForTest()
	repochange.ResetWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	dir := t.TempDir()
	var loads atomic.Int32
	cache := newStandingFlagCache(func(_ context.Context, _ string, _ []standingpatterns.Pattern) []standingpatterns.FlagCount {
		loads.Add(1)
		return nil
	})
	rules := []standingpatterns.Pattern{{ID: "one", Label: "First", Pattern: "x"}}

	cache.get(t.Context(), dir, rules)
	cache.get(t.Context(), dir, rules)
	if got := loads.Load(); got != 2 {
		t.Fatalf("loads = %d, want an uncached read without complete watch coverage", got)
	}
	if len(cache.rows) != 0 {
		t.Fatalf("rows = %d, want no entries without complete watch coverage", len(cache.rows))
	}
}

func TestStandingFlagCacheBoundsProjects(t *testing.T) {
	cache := newStandingFlagCache(func(context.Context, string, []standingpatterns.Pattern) []standingpatterns.FlagCount {
		return nil
	})
	for index := 0; index <= standingFlagProjectCap; index++ {
		cache.store(
			strconv.Itoa(index),
			standingFlagCacheRow{rulesHash: "rules"},
		)
	}
	if len(cache.rows) != standingFlagProjectCap {
		t.Fatalf("rows = %d, want %d", len(cache.rows), standingFlagProjectCap)
	}
}

func TestStandingFlagCacheCanceledCallerDoesNotStartAnalysis(t *testing.T) {
	cache := newStandingFlagCache(func(context.Context, string, []standingpatterns.Pattern) []standingpatterns.FlagCount {
		t.Error("canceled caller started analysis")
		return nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if flags := cache.get(ctx, t.TempDir(), []standingpatterns.Pattern{{ID: "one", Pattern: "x"}}); len(flags) != 0 {
		t.Fatalf("canceled caller returned flags: %+v", flags)
	}
}
