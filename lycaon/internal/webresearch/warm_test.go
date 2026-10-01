package webresearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webindex"
	"github.com/lycaon/lycaon/pkg/api"
)

// warmTestSite serves an llms.txt index entry plus a content page.
func warmTestSite(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/llms.txt":
			fmt.Fprintf(w, "- [Guide](%s/guide.md): widget frobnicator guide with benchmarks\n", srvURL(r))
		case "/guide.md":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><title>Widget frobnicator guide</title>
				<meta name="description" content="widget frobnicator guide with benchmarks"></head>
				<body><h1>Widget frobnicator guide</h1></body></html>`)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWarmCancelSessionJoinsWorkAndSuppressesCompletion(t *testing.T) {
	w := NewWarmer(nil, nil, nil, nil)
	t.Cleanup(w.Close)
	started := make(chan struct{})
	finished := make(chan struct{})
	var callback atomic.Bool
	w.warmAsync("session-1", func(api.IndexWarmingMeta) {
		callback.Store(true)
	}, func(ctx context.Context) api.IndexWarmingMeta {
		close(started)
		<-ctx.Done()
		close(finished)
		return api.IndexWarmingMeta{Pages: 1}
	})
	<-started
	w.CancelSession("session-1")
	select {
	case <-finished:
	default:
		t.Fatal("CancelSession returned before warm work finished")
	}
	if callback.Load() {
		t.Fatal("canceled warming published a completion callback")
	}
}

// warmDone waits for completion without canceling the warm.
func warmDone(t *testing.T, w *Warmer, turn, projectDir string) *api.IndexWarmingMeta {
	t.Helper()
	done := make(chan api.IndexWarmingMeta, 1)
	w.WarmDeclaredURLsAsync(turn, "project-warm-test", projectDir, "sess-warm-test", func(meta api.IndexWarmingMeta) { done <- meta })
	w.wg.Wait()
	select {
	case meta := <-done:
		return &meta
	default:
		return nil
	}
}

func warmSearchDone(t *testing.T, w *Warmer, query, projectDir string, hitURLs, residualURLs []string) *api.IndexWarmingMeta {
	t.Helper()
	done := make(chan api.IndexWarmingMeta, 1)
	w.WarmSearchAsync(query, "project-warm-test", projectDir, "sess-warm-test", "call-warm-test", hitURLs, residualURLs, 0, 0, false, func(meta api.IndexWarmingMeta) { done <- meta })
	w.wg.Wait()
	select {
	case meta := <-done:
		return &meta
	default:
		return nil
	}
}

func TestWarmSearchAsyncIngestsFromHitHosts(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(2)
	srv := warmTestSite(t)
	index := testIndex(t)

	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "cfg.yaml"))
	warming, guess := true, false
	testutil.FailErr(t, "prefs", cfg.ApplyPrefs(&warming, &guess, nil, nil))
	w := NewWarmer(index, nil, nil, cfg)
	meta := warmSearchDone(t, w, "widget frobnicator guide", "", []string{srv.URL + "/guide.md"}, nil)
	if meta == nil {
		t.Fatal("want onDone for post-search warm")
	}
	if meta.Trigger != "search" || meta.Pages == 0 {
		t.Fatalf("meta = %+v want search crawl pages", meta)
	}
	index.Flush()
	if !anyWarmedDoc(t, index, "widget frobnicator guide") {
		t.Fatal("want warmed docs from hit-host crawl")
	}
	acts, err := index.RecentActivity(context.Background(), 5)
	testutil.FailErr(t, "activity", err)
	attributed := false
	for _, a := range acts {
		if a.Trigger == "search" && a.Tier == "seed" {
			t.Fatalf("activity = %+v want no seed tier for post-search warm", acts)
		}
		if a.Trigger == "search" && a.SessionID == "sess-warm-test" && a.ToolCallID == "call-warm-test" {
			attributed = true
		}
	}
	if !attributed {
		t.Fatalf("activity = %+v want search row attributed to session and tool call", acts)
	}
}

func TestWarmDeclaredURLCrawlIngestsWithWarmedOrigin(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(2)
	srv := warmTestSite(t)
	index := testIndex(t)
	// Seed the index with an earned page for this host.
	index.QueuePage(t.Context(), webindex.Page{URL: srv.URL + "/old", Title: "widget frobnicator archive", Verified: true})
	index.Flush()

	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "cfg.yaml"))
	warming, guess := true, false
	testutil.FailErr(t, "prefs", cfg.ApplyPrefs(&warming, &guess, nil, nil))
	w := NewWarmer(index, nil, nil, cfg) // crawl tier only
	meta := warmDone(t, w, "read "+srv.URL+"/guide.md", "")
	if meta == nil {
		t.Fatal("want onDone for a warm that ingested pages")
	}
	if meta.Pages == 0 || meta.Tier != "crawl" {
		t.Fatalf("meta = %+v want crawl pages", meta)
	}
	index.Flush()
	if !anyWarmedDoc(t, index, "widget frobnicator guide") {
		t.Fatal("want warmed-origin docs ingested")
	}
	acts, err := index.RecentActivity(context.Background(), 5)
	testutil.FailErr(t, "activity", err)
	if len(acts) == 0 || acts[0].Tier != "crawl" || acts[0].Pages == 0 {
		t.Fatalf("activity = %+v", acts)
	}
}

func TestCollectWarmCrawlBasesDedupesHitAndIndex(t *testing.T) {
	index := testIndex(t)
	srv := "http://127.0.0.1:8080"
	index.QueuePage(t.Context(), webindex.Page{URL: srv + "/old", Title: "widget frobnicator archive", Verified: true})
	index.Flush()

	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	bases, hosts := w.collectWarmCrawlBases(context.Background(), "widget frobnicator", []string{srv + "/guide.md"}, 4)
	if len(hosts) != 1 || len(bases) != 1 {
		t.Fatalf("bases=%v hosts=%v want one host when hit matches index", bases, hosts)
	}
}

func TestWarmSkipsWhenDiscoveryBusy(t *testing.T) {
	resetDirectState(1)
	index := testIndex(t)
	index.QueuePage(t.Context(), webindex.Page{URL: "https://a.example/p", Title: "widget frobnicator"})
	index.Flush()
	// The test holds the only discovery slot.
	if err := acquireDirectSlot(context.Background()); err != nil {
		testutil.FailErr(t, "acquireDirectSlot failed", err)
	}
	defer releaseDirectSlot()

	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	if meta := warmDone(t, w, "read https://a.example/p", ""); meta != nil {
		t.Fatalf("meta = %+v want no card for a pure skip", meta)
	}
	index.Flush()
	acts, err := index.RecentActivity(context.Background(), 3)
	testutil.FailErr(t, "activity", err)
	if len(acts) != 1 || acts[0].SkipReason != warmSkipBusy {
		t.Fatalf("activity = %+v want busy skip recorded", acts)
	}
}

func TestWarmSeedTierRespectsHourCap(t *testing.T) {
	resetDirectState(2)
	index := testIndex(t)
	ctx := context.Background()
	for i := 0; i < defaultWarmCaps().SeedWarmsPerHour; i++ {
		ok, err := index.ReserveSeedWarm(ctx, time.Now(), time.Hour, defaultWarmCaps().SeedWarmsPerHour)
		testutil.FailErr(t, "reserve seed warm", err)
		if !ok {
			t.Fatalf("reservation %d rejected before cap", i+1)
		}
	}

	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	d := &directDiscoverer{
		index:      index,
		summarizer: &scriptedSummarizer{responses: []string{`{"seeds":[]}`}},
	}
	if !tryAcquireDirectSlot() {
		t.Fatal("slot")
	}
	defer releaseDirectSlot()
	caps := defaultWarmCaps()
	_, _, skip := w.warmSeed(ctx, d, "widget frobnicator guide", "", "", caps, caps.SeedProbes)
	if skip != warmSkipHourCap {
		t.Fatalf("skip = %q want %q", skip, warmSkipHourCap)
	}
}

func TestWarmSeedTierStreamsAndIngests(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(2)
	srv := warmTestSite(t)
	index := testIndex(t)

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
	hosts, result, skip := w.warmSeed(context.Background(), d, "widget frobnicator guide", "", "", caps, caps.SeedProbes)
	if skip != "" || result.pages == 0 || len(hosts) == 0 {
		t.Fatalf("hosts=%v result=%+v skip=%q", hosts, result, skip)
	}
	index.Flush()
	if !anyWarmedDoc(t, index, "widget frobnicator guide") {
		t.Fatal("want warmed docs from seed tier")
	}
}

func anyWarmedDoc(t *testing.T, index *webindex.Store, query string) bool {
	t.Helper()
	docs, err := index.Search(context.Background(), query, 20)
	testutil.FailErr(t, "index search", err)
	for _, d := range docs {
		if d.Origin == webindex.OriginWarmed {
			return true
		}
	}
	return false
}

func TestPreSlottedCrawlerNeverDoubleAcquires(t *testing.T) {
	resetDirectState(1)
	if !tryAcquireDirectSlot() {
		t.Fatal("slot")
	}
	defer releaseDirectSlot()
	crawler := newHostCrawlerPreSlotted(context.Background())
	// The pre-slotted crawler does not acquire the slot.
	crawler.ensureSlot()
	if crawler.slotHeld {
		t.Fatal("pre-slotted crawler must not own the slot")
	}
	// The pre-slotted crawler does not release the caller's slot.
	crawler.releaseSlot()
	if tryAcquireDirectSlot() {
		t.Fatal("caller's slot was stolen by pre-slotted release")
	}
}

func TestWarmOffModeIsInert(t *testing.T) {
	resetDirectState(2)
	index := testIndex(t)
	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "cfg.yaml"))
	warming := false
	testutil.FailErr(t, "prefs", cfg.ApplyPrefs(&warming, nil, nil, nil))
	w := NewWarmer(index, nil, nil, cfg)
	var fired atomic.Bool
	w.WarmDeclaredURLsAsync("https://docs.example/widget", "", "", "", func(api.IndexWarmingMeta) { fired.Store(true) })
	w.Close()
	index.Flush()
	acts, err := index.RecentActivity(context.Background(), 3)
	testutil.FailErr(t, "activity", err)
	if fired.Load() || len(acts) != 0 {
		t.Fatalf("fired=%v acts=%+v want fully inert when off", fired.Load(), acts)
	}
}

func TestWarmTopicHelpers(t *testing.T) {
	if got := capTokens("a b c d", 2); got != "a b" {
		t.Fatalf("capTokens = %q", got)
	}
	long := strings.Repeat("word ", 40)
	if got := clipTopic(long); len(got) > warmTopicLen {
		t.Fatalf("clipTopic len = %d", len(got))
	}
}

// warmTestCfg returns a store with the default full-warming policy.
func warmTestCfg(t *testing.T) *ConfigStore {
	t.Helper()
	return NewConfigStoreAt(filepath.Join(t.TempDir(), "warm-cfg.yaml"))
}

func TestWarmPageURLsExtractsAndCaps(t *testing.T) {
	text := "compare https://a.example/guide and https://a.example/guide (again) plus https://b.example/ref."
	got := warmPageURLs(text)
	if len(got) != 2 {
		t.Fatalf("urls = %v want deduped pair", got)
	}
	var many strings.Builder
	for i := 0; i < warmMaxPageURLs+5; i++ {
		fmt.Fprintf(&many, "https://h%d.example/p ", i)
	}
	if got := warmPageURLs(many.String()); len(got) != warmMaxPageURLs {
		t.Fatalf("urls = %d want capped at %d", len(got), warmMaxPageURLs)
	}
	if got := warmPageURLs("no links here"); got != nil {
		t.Fatalf("urls = %v want none", got)
	}
}

func TestWarmDeclaredURLsIgnoresPromptWithoutURL(t *testing.T) {
	index := testIndex(t)
	w := NewWarmer(index, nil, nil, warmTestCfg(t))
	var fired atomic.Bool
	w.WarmDeclaredURLsAsync("Secrets Tool Testing Across Real-World C", "", "", "session", func(api.IndexWarmingMeta) {
		fired.Store(true)
	})
	w.wg.Wait()
	index.Flush()
	acts, err := index.RecentActivity(context.Background(), 3)
	testutil.FailErr(t, "activity", err)
	if fired.Load() || len(acts) != 0 {
		t.Fatalf("fired=%v acts=%+v want no semantic warm for title-like prose", fired.Load(), acts)
	}
}

func TestWarmDeclaredURLCrawlsItsHost(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(2)
	srv := warmTestSite(t)
	index := testIndex(t)

	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "cfg.yaml"))
	warming, guess := true, false
	testutil.FailErr(t, "prefs", cfg.ApplyPrefs(&warming, &guess, nil, nil))
	w := NewWarmer(index, nil, nil, cfg)
	meta := warmDone(t, w, "summarize the widget frobnicator guide at "+srv.URL+"/guide.md", "")
	if meta == nil || meta.Pages == 0 {
		t.Fatalf("meta = %+v want pages from pasted-URL host crawl", meta)
	}
	index.Flush()
	if !anyWarmedDoc(t, index, "widget frobnicator guide") {
		t.Fatal("want warmed docs from the pasted URL's host")
	}
}

func TestWarmSearchResidualPagesProbed(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(2)
	srv := warmTestSite(t)
	index := testIndex(t)

	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "cfg.yaml"))
	warming, guess := true, false
	testutil.FailErr(t, "prefs", cfg.ApplyPrefs(&warming, &guess, nil, nil))
	w := NewWarmer(index, nil, nil, cfg)
	meta := warmSearchDone(t, w, "widget frobnicator guide", "", nil, []string{srv.URL + "/guide.md"})
	if meta == nil || meta.Pages == 0 {
		t.Fatalf("meta = %+v want residual pages probed", meta)
	}
	index.Flush()
	if !anyWarmedDoc(t, index, "widget frobnicator guide") {
		t.Fatal("want warmed docs from residual handoff")
	}
}

func TestWarmFetchAsyncCrawlsFetchedHost(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(2)
	srv := warmTestSite(t)
	index := testIndex(t)

	w := NewWarmer(index, nil, nil, warmTestCfg(t)) // full mode: fetch still must not seed
	done := make(chan api.IndexWarmingMeta, 1)
	w.WarmFetchAsync(srv.URL+"/guide.md", "Widget frobnicator guide", "", "", "sess-fetch", "call-fetch", func(meta api.IndexWarmingMeta) { done <- meta })
	w.wg.Wait()
	var meta *api.IndexWarmingMeta
	select {
	case m := <-done:
		meta = &m
	default:
	}
	if meta == nil || meta.Trigger != "fetch" || meta.Pages == 0 || meta.Tier != "crawl" {
		t.Fatalf("meta = %+v want fetch-triggered crawl pages", meta)
	}
	index.Flush()
	if !anyWarmedDoc(t, index, "widget frobnicator guide") {
		t.Fatal("want warmed docs from fetched host")
	}
	acts, err := index.RecentActivity(context.Background(), 5)
	testutil.FailErr(t, "activity", err)
	for _, a := range acts {
		if a.Trigger == "fetch" && a.Tier == warmTierSeed {
			t.Fatalf("activity = %+v: fetch warm must never run the seed tier", acts)
		}
	}
}
