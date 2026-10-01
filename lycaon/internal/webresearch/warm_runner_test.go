package webresearch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webindex"
)

// warmTestLive supplies explicit session presence for scheduled warming.
func warmTestLive() bool { return true }

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	testutil.FailErr(t, "mkdir "+name, os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write "+name, os.WriteFile(path, []byte(content), 0o644))
}

// Missing model capacity leaves the stack fingerprint unstamped.
func TestWarmRunnerStackWarmSkipLeavesStackUnstamped(t *testing.T) {
	resetDirectState(2)
	index := testIndex(t)
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"widget-frobnicator":"^1.0"}}`)

	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	runner := &WarmRunner{W: w, Roots: func(context.Context) ([]string, error) { return []string{dir}, nil }, Live: warmTestLive}
	runner.cycle(context.Background())
	index.Flush()
	if v, err := w.index.GetWarmState(context.Background(), "stack:"+dir); err != nil || v != "" {
		t.Fatalf("state=%q err=%v want unset after skip", v, err)
	}
	acts, err := index.RecentActivity(context.Background(), 10)
	testutil.FailErr(t, "activity", err)
	var sawStack bool
	for _, a := range acts {
		if a.Trigger == "stack_warm" && a.Tier == "seed" && a.SkipReason == warmSkipNoModel {
			sawStack = true
		}
	}
	if !sawStack {
		t.Fatalf("activity = %+v want stack_warm seed skip recorded", acts)
	}
}

func TestWarmRunnerStackWarmCrawlOnlyNoSeed(t *testing.T) {
	resetDirectState(2)
	index := testIndex(t)
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "Docs at https://example.com/docs/start\n")

	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "cfg.yaml"))
	warming, guess := true, false
	testutil.FailErr(t, "prefs", cfg.ApplyPrefs(&warming, &guess, nil, nil))
	w := NewWarmer(index, nil, nil, cfg)
	runner := &WarmRunner{W: w, Roots: func(context.Context) ([]string, error) { return []string{dir}, nil }, Live: warmTestLive}
	runner.cycle(context.Background())
	index.Flush()
	acts, err := index.RecentActivity(context.Background(), 10)
	testutil.FailErr(t, "activity", err)
	var crawl, seed int
	for _, a := range acts {
		if a.Trigger != "stack_warm" {
			continue
		}
		switch a.Tier {
		case "crawl":
			crawl++
		case "seed":
			seed++
		}
	}
	if seed != 0 {
		t.Fatalf("seed rows = %d want none in crawl_only", seed)
	}
	if crawl != 1 {
		t.Fatalf("crawl rows = %d want one stack_warm crawl", crawl)
	}
	if v, _ := index.GetWarmState(context.Background(), "stack:"+dir); v == "" {
		t.Fatal("want stack fingerprint stamped in crawl_only")
	}
}

func TestWarmRunnerStackBudgetCountsFailedProbes(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(2)
	index := testIndex(t)
	dir := t.TempDir()
	srv := warmTestSite(t)
	writeFile(t, dir, "README.md", "Docs at "+srv.URL+"/missing-page\n")
	writeFile(t, dir, "package.json", `{"dependencies":{"missing-page":"^1.0"}}`)

	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "cfg.yaml"))
	warming, guess := true, false
	testutil.FailErr(t, "prefs", cfg.ApplyPrefs(&warming, &guess, nil, nil))
	w := NewWarmer(index, nil, nil, cfg)
	runner := &WarmRunner{
		W:     w,
		Roots: func(context.Context) ([]string, error) { return []string{dir}, nil },
		Live:  warmTestLive,
	}

	spent := runner.stackWarm(context.Background(), 1, WarmingCrawlOnly)
	if spent != 1 {
		t.Fatalf("spent probes = %d want 1", spent)
	}
}

func TestWarmRunnerUnwiredLiveIsSilent(t *testing.T) {
	resetDirectState(2)
	index := testIndex(t)
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "Docs at https://example.com/docs/start\n")

	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	runner := &WarmRunner{W: w, Roots: func(context.Context) ([]string, error) { return []string{dir}, nil }}
	runner.cycle(context.Background())
	index.Flush()
	acts, err := index.RecentActivity(context.Background(), 10)
	testutil.FailErr(t, "activity", err)
	if len(acts) != 0 {
		t.Fatalf("activity = %+v want none with no presence gate wired", acts)
	}
}

func TestWarmRunnerIdleSkipsCycle(t *testing.T) {
	resetDirectState(2)
	index := testIndex(t)
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "Docs at https://example.com/docs/start\n")

	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "cfg.yaml"))
	warming, guess := true, false
	testutil.FailErr(t, "prefs", cfg.ApplyPrefs(&warming, &guess, nil, nil))
	w := NewWarmer(index, nil, nil, cfg)
	live := false
	runner := &WarmRunner{
		W:     w,
		Roots: func(context.Context) ([]string, error) { return []string{dir}, nil },
		Live:  func() bool { return live },
	}

	runner.cycle(context.Background())
	index.Flush()
	acts, err := index.RecentActivity(context.Background(), 10)
	testutil.FailErr(t, "activity", err)
	if len(acts) != 0 {
		t.Fatalf("activity = %+v want none while idle", acts)
	}
	if v, _ := index.GetWarmState(context.Background(), "stack:"+dir); v != "" {
		t.Fatalf("stack state = %q want unset while idle", v)
	}

	// The same runner warms normally once someone is using the app again.
	live = true
	runner.cycle(context.Background())
	index.Flush()
	acts, err = index.RecentActivity(context.Background(), 10)
	testutil.FailErr(t, "activity", err)
	if len(acts) == 0 {
		t.Fatal("activity = none want a stack warm once live")
	}
	if v, _ := index.GetWarmState(context.Background(), "stack:"+dir); v == "" {
		t.Fatal("want stack fingerprint stamped once live")
	}
}

func TestWarmRunnerHistoryRewarmRespectsCooldown(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(2)
	srv := warmTestSite(t)
	index := testIndex(t)
	// The loopback server uses HTTP, so its HTTPS re-crawl tests only the cooldown stamp.
	index.QueuePage(t.Context(), webindex.Page{URL: srv.URL + "/guide.md", Title: "widget frobnicator guide", Verified: true})
	index.Flush()

	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	runner := &WarmRunner{W: w, Roots: nil, Live: warmTestLive}
	runner.cycle(context.Background())
	index.Flush()

	// webindex derives hosts without ports; the rewarm state key follows it.
	stamp, err := index.GetWarmState(context.Background(), "rewarm:127.0.0.1")
	testutil.FailErr(t, "state", err)
	if stamp == "" {
		t.Fatal("want rewarm stamp after cycle")
	}
	// Second cycle inside the cooldown adds no second history_rewarm row.
	runner.cycle(context.Background())
	index.Flush()
	acts, err := index.RecentActivity(context.Background(), 20)
	testutil.FailErr(t, "activity", err)
	count := 0
	for _, a := range acts {
		if a.Trigger == "history_rewarm" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("history_rewarm rows = %d want 1 (cooldown)", count)
	}
}

func TestWarmRunnerBootstrapOnceWhenNearEmpty(t *testing.T) {
	resetDirectState(2)
	index := testIndex(t)
	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	runner := &WarmRunner{W: w, Live: warmTestLive}
	runner.cycle(context.Background())
	index.Flush()
	acts, err := index.RecentActivity(context.Background(), 10)
	testutil.FailErr(t, "activity", err)
	var boot int
	for _, a := range acts {
		if a.Trigger == "bootstrap" {
			boot++
			if a.SkipReason != warmSkipNoModel {
				t.Fatalf("bootstrap activity = %+v want model-unavailable skip", a)
			}
		}
	}
	if boot != 1 {
		t.Fatalf("bootstrap rows = %d want exactly one attempt per cycle", boot)
	}
	// A skipped cycle leaves the next cycle eligible to retry.
	if v, _ := index.GetWarmState(context.Background(), warmStateBootstrapKey); v != "" {
		t.Fatalf("bootstrap flag = %q want unset after skip", v)
	}
}

func TestWarmRunnerOffModeNoCycleWork(t *testing.T) {
	resetDirectState(2)
	index := testIndex(t)
	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "cfg.yaml"))
	warming := false
	testutil.FailErr(t, "prefs", cfg.ApplyPrefs(&warming, nil, nil, nil))
	runner := &WarmRunner{W: NewWarmer(index, nil, nil, cfg), Live: warmTestLive}
	runner.cycle(context.Background())
	index.Flush()
	acts, err := index.RecentActivity(context.Background(), 5)
	testutil.FailErr(t, "activity", err)
	if len(acts) != 0 {
		t.Fatalf("acts = %+v want none when off", acts)
	}
}

func TestWarmRunnerStarvedRewarmNoModelKeepsQueue(t *testing.T) {
	resetDirectState(2)
	index := testIndex(t)
	index.QueueSearchOutcome(t.Context(), "rare widget firmware", "", "", 0, 10)
	index.Flush()

	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	runner := &WarmRunner{W: w, Live: warmTestLive}
	runner.cycle(context.Background())
	index.Flush()

	acts, err := index.RecentActivity(context.Background(), 10)
	testutil.FailErr(t, "activity", err)
	var sawStarved bool
	for _, a := range acts {
		if a.Trigger == "starved_rewarm" {
			sawStarved = true
			if a.SkipReason != warmSkipNoModel {
				t.Fatalf("activity = %+v want model-unavailable skip", a)
			}
		}
	}
	if !sawStarved {
		t.Fatalf("activity = %+v want starved_rewarm attempt recorded", acts)
	}
	queued, err := index.StarvedQueries(context.Background(), 5)
	testutil.FailErr(t, "starved", err)
	if len(queued) != 1 {
		t.Fatalf("starved = %v want query still queued after skip", queued)
	}
}

// Transient seed failures leave the query eligible for retry.
func TestWarmSeedTransientErrorSkip(t *testing.T) {
	testutil.SkipIfShort(t, "warm seed retry timing")
	resetDirectState(2)
	index := testIndex(t)
	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	if !tryAcquireDirectSlot() {
		t.Fatal("slot")
	}
	defer releaseDirectSlot()
	d := &directDiscoverer{
		index:      index,
		origin:     webindex.OriginWarmed,
		summarizer: &errSummarizer{err: fmt.Errorf("seed timeout")},
	}
	caps := w.caps()
	_, _, skip := w.warmSeed(context.Background(), d, "rare widget firmware", "", "", caps, caps.SeedProbes)
	if skip == "" || skip == warmSkipHourCap || skip == warmSkipNoModel {
		t.Fatalf("skip = %q want transient seed error", skip)
	}
}

// The invalid host isolates crawl-target derivation from network access.
func TestWarmRunnerStackWarmCrawlsRegistryDocs(t *testing.T) {
	resetDirectState(2)
	index := testIndex(t)
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\nrequire github.com/foo/bar v1.0.0\n")
	configtest.Overlay(t, map[config.Rel]string{
		config.StackRegistryDocs: "ecosystems:\n  go: https://registry.invalid/{name}\n",
	})

	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "cfg.yaml"))
	warming, guess := true, false
	testutil.FailErr(t, "prefs", cfg.ApplyPrefs(&warming, &guess, nil, nil))
	w := NewWarmer(index, nil, nil, cfg)
	runner := &WarmRunner{
		W:     w,
		Roots: func(context.Context) ([]string, error) { return []string{dir}, nil },
		Live:  warmTestLive,
	}
	runner.cycle(context.Background())
	index.Flush()

	acts, err := index.RecentActivity(context.Background(), 10)
	testutil.FailErr(t, "activity", err)
	var hosts []string
	for _, a := range acts {
		if a.Trigger == "stack_warm" && a.Tier == "crawl" {
			hosts = append(hosts, a.Hosts...)
		}
	}
	found := false
	for _, h := range hosts {
		if h == "registry.invalid" {
			found = true
		}
	}
	if !found {
		t.Fatalf("stack crawl hosts = %v want registry host derived from go.mod dep", hosts)
	}
}

func TestWarmRunnerRunsFirstCycleImmediately(t *testing.T) {
	resetDirectState(2)
	index := testIndex(t)
	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	runner := &WarmRunner{W: w, Interval: time.Hour, Live: warmTestLive}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = runner.Run(ctx)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for {
		index.Flush()
		acts, err := index.RecentActivity(context.Background(), 5)
		testutil.FailErr(t, "activity", err)
		if len(acts) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no activity from immediate first cycle")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
