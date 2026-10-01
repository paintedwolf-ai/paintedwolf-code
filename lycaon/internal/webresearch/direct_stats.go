package webresearch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
)

// wrlog is the package logger. Each web_search logs one "web_search done" line
// with per-provider durations; each direct chain logs its phase split.
var wrlog = observability.LazyComponent("webresearch")

// logQueryLen bounds the query text carried on log lines.
const logQueryLen = 80

func clipLogText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > logQueryLen {
		return s[:logQueryLen]
	}
	return s
}

func newSearchID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "s-anon"
	}
	return "s-" + hex.EncodeToString(b[:])
}

// searchStats accumulates one search chain's pacing, robots, and probe latency
// across goroutines. It rides the context; methods are nil-safe for paths with
// no collector (e.g. fetch_url).
type searchStats struct {
	searchID string
	phase    atomic.Value // string: memory | seed | crawl | frontier

	politeWaitNs       atomic.Int64
	robotsNs           atomic.Int64
	robotsFetches      atomic.Int64
	probeNs            atomic.Int64
	probeMaxNs         atomic.Int64
	probes             atomic.Int64
	crawlFetchNs       atomic.Int64
	crawlFetches       atomic.Int64
	crawlFetchCanceled atomic.Int64
	crawlFetchCapped   atomic.Int64
	crawlFetchAttempts atomic.Int64

	seedCacheHit    atomic.Bool // pickSeeds plan cache (background warm)
	seedHedge       atomic.Bool
	seedHedgeWin    atomic.Bool
	providerSeedCut atomic.Value // string: why remaining provider seed probes were canceled
	frontierCut     atomic.Value // string: plateau (frontier early exit)

	memoryCandidates atomic.Int64
	memoryProbes     atomic.Int64
	memoryHits       atomic.Int64
	frontierRounds   atomic.Int64
	probesSpent      atomic.Int64
	probeBudget      atomic.Int64
	crawlHosts       atomic.Int64

	channelMu sync.Mutex
	channels  map[string]*channelStat

	// residualMu guards residualURLs: ranked-but-unprobed frontier candidates
	// the chain hands to the post-search warm.
	residualMu   sync.Mutex
	residualURLs []string

	directMu           sync.Mutex
	directStrongHits   int
	directMaxResults   int
	directParticipated bool
}

// setResiduals records the chain-end residual candidate URLs.
func (s *searchStats) setResiduals(urls []string) {
	if s == nil {
		return
	}
	s.residualMu.Lock()
	defer s.residualMu.Unlock()
	s.residualURLs = urls
}

// residuals returns the residual candidate URLs recorded by the direct chain.
func (s *searchStats) residuals() []string {
	if s == nil {
		return nil
	}
	s.residualMu.Lock()
	defer s.residualMu.Unlock()
	return append([]string(nil), s.residualURLs...)
}

// setDirectOutcome records Direct frontier strong-hit shape for post-search seed.
func (s *searchStats) setDirectOutcome(strongHits, maxResults int) {
	if s == nil {
		return
	}
	s.directMu.Lock()
	defer s.directMu.Unlock()
	s.directParticipated = true
	s.directStrongHits = strongHits
	s.directMaxResults = maxResults
}

func (s *searchStats) directOutcome() (strongHits, maxResults int, participated bool) {
	if s == nil {
		return 0, 0, false
	}
	s.directMu.Lock()
	defer s.directMu.Unlock()
	return s.directStrongHits, s.directMaxResults, s.directParticipated
}

type channelStat struct {
	candidates int
	hosts      int
	elapsedMs  int64
	err        string
}

func (s *searchStats) noteChannelContribution(id string, candidates, hosts int) {
	if s == nil || id == "" {
		return
	}
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	if s.channels == nil {
		s.channels = make(map[string]*channelStat)
	}
	st := s.channels[id]
	if st == nil {
		st = &channelStat{}
		s.channels[id] = st
	}
	st.candidates += candidates
	st.hosts += hosts
}

func (s *searchStats) noteChannelDone(id string, elapsed time.Duration, err error) {
	if s == nil || id == "" {
		return
	}
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	if s.channels == nil {
		s.channels = make(map[string]*channelStat)
	}
	st := s.channels[id]
	if st == nil {
		st = &channelStat{}
		s.channels[id] = st
	}
	st.elapsedMs = elapsed.Milliseconds()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		st.err = err.Error()
	}
	wrlog.Info("seed channel done",
		"search_id", s.searchID,
		"channel", id,
		"candidates", st.candidates,
		"hosts", st.hosts,
		"duration_ms", st.elapsedMs,
		"err", st.err,
	)
}

func (s *searchStats) channelSummary() string {
	if s == nil {
		return ""
	}
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	if len(s.channels) == 0 {
		return ""
	}
	ids := make([]string, 0, len(s.channels))
	for id := range s.channels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		st := s.channels[id]
		part := fmt.Sprintf("%s=%dc/%dh/%dms", id, st.candidates, st.hosts, st.elapsedMs)
		if st.err != "" {
			part += "(" + st.err + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

// noteMemoryPhase records the phase-1 outcome: candidates admitted from index
// memory, probes spent on them, and hits they yielded.
func (s *searchStats) noteMemoryPhase(candidates, probes, hits int) {
	if s == nil {
		return
	}
	s.memoryCandidates.Store(int64(candidates))
	s.memoryProbes.Store(int64(probes))
	s.memoryHits.Store(int64(hits))
}

// noteFrontier records the chain-end frontier shape.
func (s *searchStats) noteFrontier(rounds, spent, budget, hosts int) {
	if s == nil {
		return
	}
	s.frontierRounds.Store(int64(rounds))
	s.probesSpent.Store(int64(spent))
	s.probeBudget.Store(int64(budget))
	s.crawlHosts.Store(int64(hosts))
}

type searchStatsCtxKey struct{}

func withSearchStats(ctx context.Context, st *searchStats) context.Context {
	return context.WithValue(ctx, searchStatsCtxKey{}, st)
}

func statsFrom(ctx context.Context) *searchStats {
	st, _ := ctx.Value(searchStatsCtxKey{}).(*searchStats)
	return st
}

func newSearchStats(searchID string) *searchStats {
	st := &searchStats{searchID: searchID}
	st.phase.Store("")
	return st
}

func (s *searchStats) id() string {
	if s == nil {
		return ""
	}
	return s.searchID
}

func (s *searchStats) setPhase(phase string) {
	if s == nil {
		return
	}
	s.phase.Store(phase)
}

func (s *searchStats) currentPhase() string {
	if s == nil {
		return ""
	}
	phase, _ := s.phase.Load().(string)
	return phase
}

func (s *searchStats) addPoliteWait(d time.Duration) {
	if s == nil || d <= 0 {
		return
	}
	s.politeWaitNs.Add(int64(d))
}

func (s *searchStats) addRobotsFetch(d time.Duration) {
	if s == nil {
		return
	}
	s.robotsFetches.Add(1)
	s.robotsNs.Add(int64(d))
}

func (s *searchStats) addProbe(d time.Duration) {
	if s == nil {
		return
	}
	s.probes.Add(1)
	s.probeNs.Add(int64(d))
	for {
		cur := s.probeMaxNs.Load()
		if int64(d) <= cur || s.probeMaxNs.CompareAndSwap(cur, int64(d)) {
			return
		}
	}
}

func (s *searchStats) addCrawlFetch(d time.Duration) {
	if s == nil {
		return
	}
	s.crawlFetches.Add(1)
	s.crawlFetchNs.Add(int64(d))
}

func (s *searchStats) addCrawlFetchCanceled(d time.Duration) {
	if s == nil {
		return
	}
	s.crawlFetchCanceled.Add(1)
	s.crawlFetchNs.Add(int64(d))
}

func (s *searchStats) addCrawlFetchCapped() {
	if s == nil {
		return
	}
	s.crawlFetchCapped.Add(1)
}

// crawlFetchBudget is the per-search cap on site-index HTTP fetches. Beyond
// it, fetches are skipped so a canceled search cannot inflate crawl_fetches
// with thousands of dead requests.
const crawlFetchBudget = 256

func (s *searchStats) acquireCrawlFetch() bool {
	if s == nil {
		return true
	}
	return s.crawlFetchAttempts.Add(1) <= crawlFetchBudget
}

// noteFrontierCut records why the frontier stopped probing early.
func (s *searchStats) noteFrontierCut(reason string) {
	if s == nil {
		return
	}
	s.frontierCut.Store(reason)
}

func (s *searchStats) noteProviderSeedCut(reason string) {
	if s == nil {
		return
	}
	s.providerSeedCut.Store(reason)
}

func msOf(ns int64) int64 { return ns / int64(time.Millisecond) }

// chainAttrs is the direct-chain detail attribute set for the summary log line.
func (s *searchStats) chainAttrs() []any {
	if s == nil {
		return nil
	}
	attrs := []any{
		"search_id", s.searchID,
		"memory_candidates", s.memoryCandidates.Load(),
		"memory_probes", s.memoryProbes.Load(),
		"memory_hits", s.memoryHits.Load(),
		"frontier_rounds", s.frontierRounds.Load(),
		"probes_spent", s.probesSpent.Load(),
		"probe_budget", s.probeBudget.Load(),
		"crawl_hosts", s.crawlHosts.Load(),
		"polite_wait_ms", msOf(s.politeWaitNs.Load()),
		"robots_fetches", s.robotsFetches.Load(),
		"robots_ms", msOf(s.robotsNs.Load()),
		"probes", s.probes.Load(),
		"probe_ms_total", msOf(s.probeNs.Load()),
		"probe_ms_max", msOf(s.probeMaxNs.Load()),
		"crawl_fetches", s.crawlFetches.Load(),
		"crawl_fetch_canceled", s.crawlFetchCanceled.Load(),
		"crawl_fetch_capped", s.crawlFetchCapped.Load(),
		"crawl_fetch_ms", msOf(s.crawlFetchNs.Load()),
	}
	if s.seedCacheHit.Load() || s.seedHedge.Load() {
		attrs = append(attrs,
			"seed_cache_hit", s.seedCacheHit.Load(),
			"seed_hedge", s.seedHedge.Load(),
			"seed_hedge_win", s.seedHedgeWin.Load(),
		)
	}
	if cut, _ := s.providerSeedCut.Load().(string); cut != "" {
		attrs = append(attrs, "provider_seed_cut", cut)
	}
	if cut, _ := s.frontierCut.Load().(string); cut != "" {
		attrs = append(attrs, "frontier_cut", cut)
	}
	if n := len(s.residuals()); n > 0 {
		attrs = append(attrs, "residuals", n)
	}
	if summary := s.channelSummary(); summary != "" {
		attrs = append(attrs, "channels", summary)
	}
	return attrs
}
