package webresearch

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/projectstack"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/webindex"
)

const (
	warmRunnerDefaultInterval = 10 * time.Minute
	warmRewarmCooldown        = 6 * time.Hour
	warmRewarmHostsPerCycle   = 3
	warmStarvedPerCycle       = 2
	warmBootstrapDocsFloor    = 50

	warmStateBootstrapKey = "bootstrap_done"
)

// WarmRunner schedules index warming while the app is active.
type WarmRunner struct {
	W *Warmer
	// Roots returns primary-first project roots.
	Roots            func(ctx context.Context) ([]string, error)
	ProjectIDForRoot func(ctx context.Context, root string) (string, error)
	Repo             repoinfo.Provider
	Interval         time.Duration
	// Live gates scheduled network work.
	Live func() bool
}

// Run executes warm cycles until cancellation.
func (r *WarmRunner) Run(ctx context.Context) error {
	if r == nil || r.W == nil || r.W.index == nil {
		<-ctx.Done()
		return nil
	}
	interval := r.Interval
	if interval <= 0 {
		interval = warmRunnerDefaultInterval
	}
	r.cycle(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.cycle(ctx)
		}
	}
}

// cycle spends one probe budget across scheduled warm phases.
func (r *WarmRunner) cycle(ctx context.Context) {
	w := r.W
	mode := w.mode()
	if mode == WarmingOff {
		return
	}
	if r.Live == nil || !r.Live() {
		wrlog.Debug("scheduled warm cycle skipped", "reason", warmSkipNotLive)
		return
	}
	if !tryAcquireDirectSlot() {
		// Interactive discovery holds the slot.
		wrlog.Debug("scheduled warm cycle skipped", "reason", warmSkipBusy)
		return
	}
	defer releaseDirectSlot()

	caps := w.caps()
	budget := caps.ScheduledProbes
	budget -= r.stackWarm(ctx, budget, mode)
	if mode == WarmingFull && budget > 0 {
		budget -= r.starvedRewarm(ctx, budget)
	}
	if budget > 0 {
		budget -= r.historyRewarm(ctx, budget)
	}
	if mode == WarmingFull && budget > 0 {
		_ = r.bootstrap(ctx, budget)
	}
}

// stackWarm crawls project documentation and optional stack seeds.
func (r *WarmRunner) stackWarm(ctx context.Context, budget int, mode WarmingMode) (spent int) {
	if r.Roots == nil || budget <= 0 {
		return 0
	}
	roots, err := r.Roots(ctx)
	if err != nil {
		return 0
	}
	templates := projectstack.LoadRegistryDocTemplates()
	w := r.W
	for _, root := range roots {
		if ctx.Err() != nil || spent >= budget {
			return spent
		}
		signals, err := projectstack.Collect(ctx, root, r.Repo)
		if err != nil || signals.Fingerprint == "" {
			continue
		}
		stateKey := "stack:" + root
		if prev, err := w.index.GetWarmState(ctx, stateKey); err == nil && prev == signals.Fingerprint {
			continue
		}

		query := projectstack.SeedQuery(signals)
		docURLs := append([]string(nil), signals.DocURLs...)
		docURLs = append(docURLs, projectstack.RegistryDocURLs(templates, signals.Deps)...)
		if len(docURLs) > 0 && spent < budget {
			crawlStart := time.Now()
			d := &directDiscoverer{index: w.index, origin: webindex.OriginWarmed, seedOrigin: searchOriginWarm}
			hosts, result := w.warmDocHosts(ctx, d, docURLs, query, budget-spent)
			w.index.QueueActivity(ctx, webindex.WarmActivity{
				Trigger: warmTriggerStack, Tier: warmTierCrawl, Topic: clipTopic(filepath.Base(root)),
				Hosts: hosts, Pages: result.pages,
				DurationMs: time.Since(crawlStart).Milliseconds(),
			})
			spent += result.probes
		}

		if mode == WarmingFull && query != "" {
			if spent >= budget {
				return spent
			}
			projectID := ""
			if r.ProjectIDForRoot != nil {
				projectID, _ = r.ProjectIDForRoot(ctx, root)
			}
			seedStart := time.Now()
			d := &directDiscoverer{index: w.index, origin: webindex.OriginWarmed, taskHint: query, seedOrigin: searchOriginWarm}
			caps := w.caps()
			hosts, result, skip := w.warmSeed(ctx, d, capTokens(query, warmTopicTokens), projectID, root, caps, min(caps.SeedProbes, budget-spent))
			w.index.QueueActivity(ctx, webindex.WarmActivity{
				Trigger: warmTriggerStack, Tier: warmTierSeed, Topic: clipTopic(query),
				Hosts: hosts, Pages: result.pages,
				DurationMs: time.Since(seedStart).Milliseconds(), SkipReason: skip,
			})
			if skip != "" {
				return spent
			}
			spent += result.probes
		}

		w.index.SetWarmState(ctx, stateKey, signals.Fingerprint)
	}
	return spent
}

// starvedRewarm seeds queries with too few content-bearing hits.
func (r *WarmRunner) starvedRewarm(ctx context.Context, budget int) (spent int) {
	w := r.W
	queries, err := w.index.StarvedQueries(ctx, warmStarvedPerCycle)
	if err != nil || len(queries) == 0 {
		return 0
	}
	for _, query := range queries {
		if ctx.Err() != nil || spent >= budget {
			return spent
		}
		start := time.Now()
		d := &directDiscoverer{index: w.index, origin: webindex.OriginWarmed, taskHint: query.Query, seedOrigin: searchOriginWarm}
		caps := w.caps()
		hosts, result, skip := w.warmSeed(ctx, d, capTokens(query.Query, warmTopicTokens), query.ProjectID, query.ProjectDir, caps, min(caps.SeedProbes, budget-spent))
		w.index.QueueActivity(ctx, webindex.WarmActivity{
			Trigger: warmTriggerStarved, Tier: warmTierSeed, Topic: clipTopic(query.Query),
			Hosts: hosts, Pages: result.pages,
			DurationMs: time.Since(start).Milliseconds(), SkipReason: skip,
		})
		if skip != "" {
			if skip == warmSkipHourCap || skip == warmSkipNoModel {
				// Admission failures keep the query queued.
				return spent
			}
			// Other failures also keep it queued.
			continue
		}
		w.index.MarkQueryRewarmed(ctx, query.Query, query.ProjectID)
		spent += result.probes
	}
	return spent
}

// historyRewarm refreshes stale hosts with verified documents.
func (r *WarmRunner) historyRewarm(ctx context.Context, budget int) (spent int) {
	w := r.W
	hosts, err := w.index.HostsForRewarm(ctx, warmRewarmHostsPerCycle*3)
	if err != nil || len(hosts) == 0 {
		return 0
	}
	caps := w.caps()
	d := &directDiscoverer{index: w.index, origin: webindex.OriginWarmed, seedOrigin: searchOriginWarm}
	warmed := 0
	for _, host := range hosts {
		if ctx.Err() != nil || spent >= budget || warmed >= warmRewarmHostsPerCycle {
			break
		}
		stateKey := "rewarm:" + host
		if last, err := w.index.GetWarmState(ctx, stateKey); err == nil && last != "" {
			if at, perr := time.Parse(time.RFC3339, last); perr == nil && time.Since(at) < warmRewarmCooldown {
				continue
			}
		}
		start := time.Now()
		crawler := newHostCrawlerPreSlotted(ctx)
		crawler.launch("https://"+host, nil)
		scorer := newQueryScorer(host, nil, false, CurrentPeriod())
		fr := newFrontier(caps.TurnProbes, maxHitsPerHost)
		fr.budget = min(caps.TurnProbes, budget-spent)
		w.runWarmFrontier(ctx, crawler, fr, d, scorer)
		w.index.SetWarmState(ctx, stateKey, time.Now().UTC().Format(time.RFC3339))
		w.index.QueueActivity(ctx, webindex.WarmActivity{
			Trigger: warmTriggerRewarm, Tier: warmTierCrawl, Topic: host,
			Hosts: []string{host}, Pages: len(fr.hits),
			DurationMs: time.Since(start).Milliseconds(),
		})
		spent += fr.spent
		warmed++
	}
	return spent
}

// bootstrap seeds a near-empty index once.
func (r *WarmRunner) bootstrap(ctx context.Context, budget int) int {
	w := r.W
	if done, err := w.index.GetWarmState(ctx, warmStateBootstrapKey); err != nil || done != "" {
		return 0
	}
	stats, err := w.index.Stats(ctx)
	if err != nil || stats.Docs >= warmBootstrapDocsFloor {
		return 0
	}
	query := "official documentation and reference sites for widely used programming languages and developer tools"
	start := time.Now()
	d := &directDiscoverer{index: w.index, origin: webindex.OriginWarmed, taskHint: query, seedOrigin: searchOriginWarm}
	caps := w.caps()
	hosts, result, skip := w.warmSeed(ctx, d, capTokens(query, warmTopicTokens), "", "", caps, min(caps.SeedProbes, budget))
	w.index.QueueActivity(ctx, webindex.WarmActivity{
		Trigger: warmTriggerBootstrap, Tier: warmTierSeed, Topic: clipTopic(query),
		Hosts: hosts, Pages: result.pages,
		DurationMs: time.Since(start).Milliseconds(), SkipReason: skip,
	})
	if skip == "" {
		w.index.SetWarmState(ctx, warmStateBootstrapKey, fmt.Sprintf("pages=%d", result.pages))
	}
	return result.probes
}
