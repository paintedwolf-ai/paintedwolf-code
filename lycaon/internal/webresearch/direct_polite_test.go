package webresearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoliteAcquirePacesRequestStarts(t *testing.T) {
	resetPoliteness(40 * time.Millisecond)
	t.Cleanup(func() { resetPoliteness(5 * time.Millisecond) })

	start := time.Now()
	var starts []time.Duration
	for i := 0; i < 3; i++ {
		release, err := politeAcquire(t.Context(), "paced.example")
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		starts = append(starts, time.Since(start))
		release()
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i] - starts[i-1]; gap < 30*time.Millisecond {
			t.Fatalf("starts = %v want >= interval between request starts", starts)
		}
	}
}

func TestPoliteAcquireBoundsInFlight(t *testing.T) {
	resetPoliteness(time.Millisecond)
	var inFlight, peak int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := politeAcquire(t.Context(), "bounded.example")
			if err != nil {
				return
			}
			n := atomic.AddInt64(&inFlight, 1)
			for {
				p := atomic.LoadInt64(&peak)
				if n <= p || atomic.CompareAndSwapInt64(&peak, p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt64(&inFlight, -1)
			release()
		}()
	}
	wg.Wait()
	if peak > politeHostMaxInFlight {
		t.Fatalf("peak in-flight = %d want <= %d", peak, politeHostMaxInFlight)
	}
}

func TestPoliteRecordCooldownOn429(t *testing.T) {
	resetPoliteness(time.Millisecond)
	politeRecord("busy.example", http.StatusTooManyRequests, "2")
	if _, err := politeAcquire(t.Context(), "busy.example"); err == nil {
		t.Fatal("want cooldown error after 429")
	}
	// Host cooldowns are isolated.
	release, err := politeAcquire(t.Context(), "fine.example")
	if err != nil {
		t.Fatalf("other host: %v", err)
	}
	release()
}

func TestPoliteRecordForbiddenStrikes(t *testing.T) {
	resetPoliteness(time.Millisecond)
	politeRecord("waf.example", http.StatusForbidden, "")
	if _, err := politeAcquire(t.Context(), "waf.example"); err != nil {
		t.Fatalf("one 403 must not cool down: %v", err)
	}
	politeRecord("waf.example", http.StatusForbidden, "")
	if _, err := politeAcquire(t.Context(), "waf.example"); err == nil {
		t.Fatal("want cooldown after consecutive 403s")
	}
	// Success clears forbidden strikes.
	resetPoliteness(time.Millisecond)
	politeRecord("ok.example", http.StatusForbidden, "")
	politeRecord("ok.example", http.StatusOK, "")
	politeRecord("ok.example", http.StatusForbidden, "")
	if _, err := politeAcquire(t.Context(), "ok.example"); err != nil {
		t.Fatalf("interleaved success must clear strikes: %v", err)
	}
}

func TestRobotsCrawlDelayParsedAndCapped(t *testing.T) {
	body := "User-agent: *\nCrawl-delay: 2\n\nUser-agent: other\ncrawl-delay: 4.5\n"
	if d := robotsCrawlDelay(body); d != 4500*time.Millisecond {
		t.Fatalf("delay = %v want max declared value", d)
	}
	resetPoliteness(time.Millisecond)
	politeSetCrawlDelay("slow.example", time.Hour)
	if g := gateFor("slow.example"); g.interval != politeMaxCrawlDelay {
		t.Fatalf("interval = %v want capped at %v", g.interval, politeMaxCrawlDelay)
	}
}

func TestProbeHonorsRobotsDisallow(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	var blockedHits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: *\nDisallow: /private/\n")
		case "/private/secret":
			atomic.AddInt64(&blockedHits, 1)
			fmt.Fprint(w, "secret widget frobnicator data")
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	probe := fetchPageProbe(context.Background(), srv.URL+"/private/secret")
	if probe.live {
		t.Fatal("robots-disallowed probe must not report live")
	}
	if atomic.LoadInt64(&blockedHits) != 0 {
		t.Fatalf("blocked path fetched %d times, want 0", blockedHits)
	}
}

func TestProbeHonorsRobotsOnRedirectTarget(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	var blockedHits int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: *\nDisallow: /private\n")
		case "/private":
			atomic.AddInt64(&blockedHits, 1)
			fmt.Fprint(w, "private content")
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, target.URL+"/private", http.StatusFound)
	}))
	t.Cleanup(source.Close)

	probe := fetchPageProbe(context.Background(), source.URL+"/start")
	if probe.live || !probe.robotsBlocked {
		t.Fatalf("probe = %+v want redirect target blocked by robots", probe)
	}
	if got := atomic.LoadInt64(&blockedHits); got != 0 {
		t.Fatalf("redirect target fetched %d times, want 0", got)
	}
}

func TestDiscoverFeedURLsFromDeclaration(t *testing.T) {
	body := `<html><head>
		<link rel="alternate" type="application/rss+xml" href="/custom/feed.xml">
		<link rel="alternate" type="application/atom+xml" href="https://cdn.example/atom.xml">
		<link rel="stylesheet" href="/style.css">
	</head><body></body></html>`
	feeds := discoverFeedURLs(body, "https://site.example")
	if len(feeds) != 2 {
		t.Fatalf("feeds = %v want both declared feeds", feeds)
	}
	if feeds[0] != "https://site.example/custom/feed.xml" {
		t.Fatalf("feeds[0] = %q want relative href resolved", feeds[0])
	}
}

// TestDiscoverSiteIndexesNoBlindFeedProbes limits crawls to declared and well-known paths.
func TestDiscoverSiteIndexesNoBlindFeedProbes(t *testing.T) {
	allowLoopbackFetch(t)
	resetDirectState(1)
	var mu sync.Mutex
	paths := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	discoverSiteIndexes(context.Background(), srv.URL, []string{"widget"})
	mu.Lock()
	defer mu.Unlock()
	allowed := map[string]struct{}{"/robots.txt": {}, "/llms.txt": {}, "/sitemap.xml": {}, "/": {}}
	for path := range paths {
		if _, ok := allowed[path]; !ok {
			t.Fatalf("unexpected probe path %q (blind guessing): %v", path, paths)
		}
	}
}
