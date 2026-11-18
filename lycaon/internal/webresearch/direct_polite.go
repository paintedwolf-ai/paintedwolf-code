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

var errHostCoolingDown = errors.New("host cooling down after rate-limit or block")
var errRobotsBlocked = errors.New("blocked by robots.txt")

type hostGate struct {
	users      int
	sem        chan struct{}
	pace       chan struct{}
	mu         sync.Mutex
	nextAt     time.Time
	interval   time.Duration
	coolUntil  time.Time
	strikes403 int
	touched    time.Time
}

func (crawler *Crawler) gateFor(host string) *hostGate {
	crawler.gatesMu.Lock()
	defer crawler.gatesMu.Unlock()
	return crawler.gateForLocked(host)
}

func (crawler *Crawler) gateForLocked(host string) *hostGate {
	host = strings.ToLower(strings.TrimSpace(host))
	g, ok := crawler.gates[host]
	if !ok {
		if len(crawler.gates) >= politeMaxHosts {
			oldestKey, oldestAt := "", time.Time{}
			for k, e := range crawler.gates {
				if e.users != 0 {
					continue
				}
				if oldestKey == "" || e.touched.Before(oldestAt) {
					oldestKey, oldestAt = k, e.touched
				}
			}
			delete(crawler.gates, oldestKey)
		}
		g = &hostGate{
			sem:      make(chan struct{}, politeHostMaxInFlight),
			pace:     make(chan struct{}, 1),
			interval: crawler.interval,
		}
		crawler.gates[host] = g
	}
	g.touched = time.Now()
	return g
}

// Active requests keep the same gate through pacing and response recording.
func (crawler *Crawler) retainGate(host string) (*hostGate, func()) {
	crawler.gatesMu.Lock()
	gate := crawler.gateForLocked(host)
	gate.users++
	crawler.gatesMu.Unlock()
	return gate, sync.OnceFunc(func() {
		crawler.gatesMu.Lock()
		gate.users--
		if gate.users == 0 && len(crawler.gates) > politeMaxHosts {
			delete(crawler.gates, strings.ToLower(strings.TrimSpace(host)))
		}
		crawler.gatesMu.Unlock()
	})
}

// politeAcquire reserves one paced request slot for a host.
func (crawler *Crawler) politeAcquire(ctx context.Context, host string) (func(), error) {
	waitStart := time.Now()
	defer func() { statsFrom(ctx).addPoliteWait(time.Since(waitStart)) }()
	g, drop := crawler.retainGate(host)
	g.mu.Lock()
	cooling := time.Now().Before(g.coolUntil)
	g.mu.Unlock()
	if cooling {
		drop()
		return nil, errHostCoolingDown
	}
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
	release := sync.OnceFunc(func() { <-g.sem; drop() })
	select {
	case g.pace <- struct{}{}:
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
	defer func() { <-g.pace }()
	g.mu.Lock()
	start := g.nextAt
	g.mu.Unlock()
	if wait := time.Until(start); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	if time.Now().Before(g.coolUntil) {
		release()
		return nil, errHostCoolingDown
	}
	g.nextAt = time.Now().Add(g.interval)
	return release, nil
}

// politeRecord folds a response status into the host's gate: rate-limit and
// block responses start a cooldown, success clears forbidden strikes.
func (crawler *Crawler) politeRecord(host string, status int, retryAfter string) {
	g := crawler.gateFor(host)
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
func (crawler *Crawler) politeSetCrawlDelay(host string, d time.Duration) {
	if d <= 0 {
		return
	}
	if d > politeMaxCrawlDelay {
		d = politeMaxCrawlDelay
	}
	g := crawler.gateFor(host)
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

func (crawler *Crawler) storeRobotsBody(host, body string) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return
	}
	crawler.robots.put(host, body)
}

func (crawler *Crawler) cachedRobotsBody(host string) (string, bool) {
	return crawler.robots.get(strings.ToLower(strings.TrimSpace(host)))
}

// probeAllowedByRobots gates crawl-initiated page probes on the target host's
// robots.txt, fetching and caching it on first contact with a host. An empty
// or unreachable robots file allows everything.
func (crawler *Crawler) probeAllowedByRobots(ctx context.Context, pageURL string) bool {
	host := strings.ToLower(urlHost(pageURL))
	if host == "" {
		return false
	}
	body, ok := crawler.cachedRobotsBody(host)
	if !ok {
		// First contact with this host: the robots fetch is charged against
		// the caller's probe window, so account for it separately.
		fetchStart := time.Now()
		base := normalizeSiteBase(pageURL)
		body = crawler.fetchRobotsTxt(ctx, base)
		statsFrom(ctx).addRobotsFetch(time.Since(fetchStart))
		crawler.storeRobotsBody(host, body)
		crawler.politeSetCrawlDelay(host, robotsCrawlDelay(body))
	}
	return urlAllowedByRobots(body, pageURL)
}

func (crawler *Crawler) politeGuardedGet(ctx context.Context, u *url.URL, accept string, limit int64, checkRobots bool) (*http.Response, string, error) {
	beforeHop := func(ctx context.Context, hopURL *url.URL) (func(), error) {
		if checkRobots && !crawler.probeAllowedByRobots(ctx, hopURL.String()) {
			return nil, errRobotsBlocked
		}
		return crawler.politeAcquire(ctx, hopURL.Host)
	}
	recordHop := func(hopURL *url.URL, resp *http.Response) {
		crawler.politeRecord(hopURL.Host, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	return crawler.guardedGetWithHopPolicy(ctx, u, accept, limit, beforeHop, recordHop)
}
