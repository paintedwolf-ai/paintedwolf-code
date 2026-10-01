package webresearch

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webindex"
)

func TestWarmQueryUnderfilledSearchSeedSkipsWithoutModel(t *testing.T) {
	resetDirectState(1)
	index := testIndex(t)
	query := "useEffect react hooks"
	index.QueueSearchOutcome(t.Context(), query, "", "", 0, 10)
	index.Flush()

	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	_ = w.warmQuery(context.Background(), warmRequest{
		trigger:            warmTriggerSearch,
		query:              query,
		taskHint:           query,
		outcomeQuery:       query,
		strongHits:         0,
		maxResults:         10,
		directParticipated: true,
	})
	index.Flush()
	time.Sleep(20 * time.Millisecond)

	acts, err := index.RecentActivity(context.Background(), 10)
	testutil.FailErr(t, "activity", err)
	found := false
	for _, a := range acts {
		if a.Trigger == warmTriggerSearchSeed && a.Tier == warmTierSeed {
			found = true
			if a.SkipReason != warmSkipNoModel {
				t.Fatalf("skip = %q want %q", a.SkipReason, warmSkipNoModel)
			}
		}
	}
	if !found {
		t.Fatalf("activity = %+v want search_seed skip", acts)
	}
	starved, err := index.StarvedQueries(context.Background(), 5)
	testutil.FailErr(t, "starved", err)
	if len(starved) != 1 || starved[0].Query != query {
		t.Fatalf("starved = %v want query still queued after seed skip", starved)
	}
}

func TestWarmQueryFilledSearchDoesNotSeed(t *testing.T) {
	resetDirectState(1)
	index := testIndex(t)
	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "cfg.yaml"))
	warming, guess := true, true
	testutil.FailErr(t, "prefs", cfg.ApplyPrefs(&warming, &guess, nil, nil))
	w := NewWarmer(index, nil, nil, cfg)

	_ = w.warmQuery(context.Background(), warmRequest{
		trigger:            warmTriggerSearch,
		query:              "widget guide",
		taskHint:           "widget guide",
		outcomeQuery:       "widget guide",
		strongHits:         10,
		maxResults:         10,
		directParticipated: true,
	})
	index.Flush()
	time.Sleep(20 * time.Millisecond)

	acts, err := index.RecentActivity(context.Background(), 10)
	testutil.FailErr(t, "activity", err)
	for _, a := range acts {
		if a.Trigger == warmTriggerSearchSeed {
			t.Fatalf("activity = %+v want no search_seed when filled", acts)
		}
	}
}

func TestSearchSeedSuccessMarksQueryRewarmed(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	srv := warmTestSite(t)
	index := testIndex(t)
	query := "widget frobnicator guide"
	index.QueueSearchOutcome(t.Context(), query, "", "", 1, 10)
	index.Flush()

	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	d := &directDiscoverer{
		summarizer: &scriptedSummarizer{responses: []string{fmt.Sprintf(`{"seeds":[%q]}`, srv.URL)}},
		index:      index,
		origin:     webindex.OriginWarmed,
	}
	if !tryAcquireDirectSlot() {
		t.Fatal("slot")
	}
	defer releaseDirectSlot()
	caps := defaultWarmCaps()
	_, _, skip := w.warmSeed(context.Background(), d, query, "", "", caps, caps.SeedProbes)
	if skip != "" {
		t.Fatalf("warmSeed skip = %q", skip)
	}
	index.MarkQueryRewarmed(t.Context(), query, "")
	index.Flush()
	time.Sleep(20 * time.Millisecond)

	starved, err := index.StarvedQueries(context.Background(), 5)
	testutil.FailErr(t, "starved", err)
	if len(starved) != 0 {
		t.Fatalf("starved = %v want cleared after MarkQueryRewarmed", starved)
	}
}
