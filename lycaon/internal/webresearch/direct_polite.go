package webresearch

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Crawl controls bound concurrency, pace starts, and honor host signals.
const (
	politeHostMaxInFlight = 2
	// politeMaxCrawlDelay bounds declared crawl delays.
	politeMaxCrawlDelay = 10 * time.Second
	// Busy cooldowns are bounded when Retry-After is absent or excessive.
	politeCooldownBusy = 30 * time.Second
	politeCooldownMax  = 15 * time.Minute
	// Repeated forbidden responses pause the host.
	politeForbiddenStrikes  = 2
	politeCooldownForbidden = 15 * time.Minute
	politeMaxHosts          = 512
)

// politeHostInterval spaces request starts for one host.
var politeHostInterval = 500 * time.Millisecond

var errHostCoolingDown = errors.New("host cooling down after rate-limit or block")
var errRobotsBlocked = errors.New("blocked by robots.txt")

type hostGate struct {
	sem        chan struct{}
	pace       sync.Mutex
	mu         sync.Mutex
	nextAt     time.Time
	interval   time.Duration
	coolUntil  time.Time
	strikes403 int
	touched    time.Time
}

var politeGates = struct {
	sync.Mutex
	m map[string]*hostGate
}{m: make(map[string]*hostGate)}

func gateFor(host string) *hostGate {
	host = strings.ToLower(strings.TrimSpace(host))
	politeGates.Lock()
	defer politeGates.Unlock()
	g, ok := politeGates.m[host]
	if !ok {
		if len(politeGates.m) >= politeMaxHosts {
			oldestKey, oldestAt := "", time.Time{}
			for k, e := range politeGates.m {
				if oldestKey == "" || e.touched.Before(oldestAt) {
					oldestKey, oldestAt = k, e.touched
				}
			}
			delete(politeGates.m, oldestKey)
		}
		g = &hostGate{
			sem:      make(chan struct{}, politeHostMaxInFlight),
			interval: politeHostInterval,
		}
		politeGates.m[host] = g
	}
	g.touched = time.Now()
	return g
}

// politeAcquire reserves one paced request slot for a host.
func politeAcquire(ctx context.Context, host string) (func(), error) {
	waitStart := time.Now()
	defer func() { statsFrom(ctx).addPoliteWait(time.Since(waitStart)) }()
	g := gateFor(host)
	g.mu.Lock()
	cooling := time.Now().Before(g.coolUntil)
	g.mu.Unlock()
	if cooling {
		return nil, errHostCoolingDown
	}
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	g.pace.Lock()
	g.mu.Lock()
	now := time.Now()
	start := g.nextAt
	if start.Before(now) {
		start = now
	}
	g.mu.Unlock()
	if wait := time.Until(start); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			g.pace.Unlock()
			<-g.sem
			return nil, ctx.Err()
		}
	}
	g.mu.Lock()
	g.nextAt = time.Now().Add(g.interval)
	g.mu.Unlock()
	g.pace.Unlock()
	return func() { <-g.sem }, nil
}

// politeRecord folds a response status into the host's gate: rate-limit and
// block responses start a cooldown, success clears forbidden strikes.
func politeRecord(host string, status int, retryAfter string) {
	g := gateFor(host)
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
		cool := parseRetryAfter(retryAfter)
		if cool <= 0 {
			cool = politeCooldownBusy
		}
		if cool > politeCooldownMax {
			cool = politeCooldownMax
		}
		g.coolUntil = time.Now().Add(cool)
	case status == http.StatusForbidden:
		g.strikes403++
		if g.strikes403 >= politeForbiddenStrikes {
			g.coolUntil = time.Now().Add(politeCooldownForbidden)
		}
	case status >= 200 && status < 400:
		g.strikes403 = 0
	}
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		return time.Until(at)
	}
	return 0
}

// politeSetCrawlDelay applies a robots-declared Crawl-delay to the host's
// pacing interval — never below the default, capped at politeMaxCrawlDelay.
func politeSetCrawlDelay(host string, d time.Duration) {
	if d <= 0 {
		return
	}
	if d > politeMaxCrawlDelay {
		d = politeMaxCrawlDelay
	}
	g := gateFor(host)
	g.mu.Lock()
	if d > g.interval {
		g.interval = d
	}
	g.mu.Unlock()
}

var crawlDelayRe = regexp.MustCompile(`(?im)^\s*crawl-delay\s*:\s*([0-9]+(?:\.[0-9]+)?)`)

// robotsCrawlDelay returns the largest Crawl-delay declared anywhere in the
// robots body — conservative: any group's ask is honored.
func robotsCrawlDelay(body string) time.Duration {
	var maxDelay time.Duration
	for _, m := range crawlDelayRe.FindAllStringSubmatch(body, -1) {
		if secs, err := strconv.ParseFloat(m[1], 64); err == nil {
			if d := time.Duration(secs * float64(time.Second)); d > maxDelay {
				maxDelay = d
			}
		}
	}
	return maxDelay
}

// robotsBodies caches robots.txt per host so frontier probes (which can land
// on hosts no index crawl visited) can check permission without re-fetching.
var robotsBodies = newTTLCache[string](siteIndexCacheTTL, politeMaxHosts)

func storeRobotsBody(host, body string) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return
	}
	robotsBodies.put(host, body)
}

func cachedRobotsBody(host string) (string, bool) {
	return robotsBodies.get(strings.ToLower(strings.TrimSpace(host)))
}

// probeAllowedByRobots gates crawl-initiated page probes on the target host's
// robots.txt, fetching and caching it on first contact with a host. An empty
// or unreachable robots file allows everything.
func probeAllowedByRobots(ctx context.Context, pageURL string) bool {
	host := strings.ToLower(urlHost(pageURL))
	if host == "" {
		return false
	}
	body, ok := cachedRobotsBody(host)
	if !ok {
		// First contact with this host: the robots fetch is charged against
		// the caller's probe window, so account for it separately.
		fetchStart := time.Now()
		base := normalizeSiteBase(pageURL)
		body = fetchRobotsTxt(ctx, base)
		statsFrom(ctx).addRobotsFetch(time.Since(fetchStart))
		storeRobotsBody(host, body)
		politeSetCrawlDelay(host, robotsCrawlDelay(body))
	}
	return urlAllowedByRobots(body, pageURL)
}

func politeGuardedGet(ctx context.Context, u *url.URL, accept string, checkRobots bool) (*http.Response, string, error) {
	beforeHop := func(ctx context.Context, hopURL *url.URL) (func(), error) {
		if checkRobots && !probeAllowedByRobots(ctx, hopURL.String()) {
			return nil, errRobotsBlocked
		}
		return politeAcquire(ctx, hopURL.Host)
	}
	recordHop := func(hopURL *url.URL, resp *http.Response) {
		politeRecord(hopURL.Host, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	return guardedGetWithHopPolicy(ctx, u, accept, beforeHop, recordHop)
}

// resetPoliteness clears gates and robots caches; tests shrink the pacing
// interval so httptest crawls stay fast.
func resetPoliteness(interval time.Duration) {
	if interval > 0 {
		politeHostInterval = interval
	}
	politeGates.Lock()
	politeGates.m = make(map[string]*hostGate)
	politeGates.Unlock()
	robotsBodies.reset()
}
