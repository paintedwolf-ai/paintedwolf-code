package webresearch

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/egressgate"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// dropIncompleteTail removes the last seed and last lead of a partial plan —
// on a mid-stream buffer they may be truncated fragments.
func dropIncompleteTail(p seedPlan) seedPlan {
	if len(p.seeds) > 0 {
		p.seeds = p.seeds[:len(p.seeds)-1]
	}
	if len(p.leads) > 0 {
		p.leads = p.leads[:len(p.leads)-1]
	}
	return p
}

func parseSeedsJSON(raw string) (seedPlan, error) {
	var plan seedPlan
	var candidates []string
	if obj := extractJSONObject(raw); obj != "" {
		var payload struct {
			Seeds  []string `json:"seeds"`
			Fresh  bool     `json:"fresh"`
			Expand []string `json:"expand"`
			Leads  []struct {
				Title string `json:"title"`
				Host  string `json:"host"`
				// URL absorbs a model-volunteered article path; only its host is
				// used, since result URLs are discovered live.
				URL string `json:"url"`
			} `json:"leads"`
		}
		if err := json.Unmarshal([]byte(obj), &payload); err == nil {
			plan.fresh = payload.Fresh
			plan.expand = payload.Expand
			candidates = payload.Seeds
			for _, lead := range payload.Leads {
				if len(plan.leads) >= maxLeadsPerQuery {
					break
				}
				title := strings.TrimSpace(lead.Title)
				host := normalizeLeadHost(lead.Host)
				if host == "" {
					if u := normalizePageURL(lead.URL); u != "" {
						host = strings.ToLower(urlHost(u))
					}
				}
				if title == "" && host == "" {
					continue
				}
				plan.leads = append(plan.leads, seedLead{title: title, host: host})
			}
		}
	}
	if len(candidates) == 0 {
		// The model ignored the JSON contract (prose, or freestyle keys like
		// "sites") — scrape any bare URLs out of the raw text instead.
		candidates = scrapeURLs(raw)
	}
	seen := make(map[string]struct{})
	for _, s := range candidates {
		seed := normalizeSeed(s)
		if seed == "" {
			continue
		}
		key := strings.ToLower(seed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		plan.seeds = append(plan.seeds, seed)
		if len(plan.seeds) >= maxSeedsPerQuery {
			break
		}
	}
	// Leads alone are a valid plan: mergeLeadHosts turns lead hosts into seeds,
	// and the prompt asks for seeds only as roots not already covered by a lead.
	if len(plan.seeds) == 0 && len(plan.leads) == 0 {
		return seedPlan{}, fmt.Errorf("no valid seeds")
	}
	return plan, nil
}

func normalizeSeed(raw string) string {
	page := normalizePageURL(raw)
	if page == "" {
		return ""
	}
	u, err := url.Parse(page)
	if err != nil {
		return ""
	}
	path := strings.TrimSuffix(u.Path, "/")
	if path == "" {
		return normalizeSiteBase(page)
	}
	return page
}

func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```")
		if end := strings.LastIndex(raw, "```"); end >= 0 {
			raw = raw[:end]
		}
		raw = strings.TrimSpace(raw)
	}
	start := strings.Index(raw, "{")
	if start < 0 {
		return ""
	}
	end := strings.LastIndex(raw, "}")
	if end > start {
		return raw[start : end+1]
	}
	// Small models drop trailing closers or get cut by token limits — repair.
	return balanceJSON(strings.TrimSpace(raw[start:]))
}

// balanceJSON appends the closers a truncated JSON document is missing.
func balanceJSON(s string) string {
	var stack []byte
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	var b strings.Builder
	b.WriteString(s)
	if inStr {
		b.WriteByte('"')
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			b.WriteByte('}')
		} else {
			b.WriteByte(']')
		}
	}
	return b.String()
}

var scrapeURLRe = regexp.MustCompile(`https?://[^\s"'<>\\)\]}]+`)

// scrapeURLs pulls bare URLs out of prose when the model ignored the JSON contract.
func scrapeURLs(raw string) []string {
	matches := scrapeURLRe.FindAllString(raw, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.TrimRight(m, ".,;:!?"))
	}
	return out
}

// providerSeedGraceTimeout bounds how long direct search waits for more catalog
// provider seed probes once other seed work has already landed (candidates or hits).
var providerSeedGraceTimeout = 1 * time.Second

// providerSeedZeroHitTimeout caps provider seed fan-out when nothing has
// contributed yet, so dead backends don't hold the full per-provider window.
var providerSeedZeroHitTimeout = 3 * time.Second

// discoveryWait is the search loop's blocking edge: it decides whether the
// search is finished, whether to wait for more contributions, and when to cut
// slow provider seed probes once other seed work has landed.
type discoveryWait struct {
	fr                 *frontier
	crawler            *hostCrawler
	stats              *searchStats
	contribWake        chan struct{}
	providerPending    *atomic.Int64
	providerSeedCancel context.CancelFunc
	providerSeedStart  time.Time
	providerGraceStart time.Time
}

func (w *discoveryWait) seedWorkReady() bool {
	if w.fr.hitCount() > 0 {
		return true
	}
	// Index-memory admits alone do not start the provider-seed grace, so weak
	// memory noise cannot cut live seeds before any probe runs.
	return w.fr.hasNonMemoryCandidate()
}

func (w *discoveryWait) cutProviderSeeds(reason string) {
	w.stats.noteProviderSeedCut(reason)
	if w.providerPending.Load() == 0 {
		return
	}
	w.providerSeedCancel()
}

// pause blocks until new work can arrive. It returns false when the search
// should stop: all provider channels and crawls are done, or provider seeds
// were cut after grace / zero-hit cap.
func (w *discoveryWait) pause(ctx context.Context) bool {
	crawlBusy := w.crawler.pending() || w.crawler.undrained()
	if w.providerPending.Load() == 0 && !crawlBusy {
		return false
	}
	if w.providerPending.Load() > 0 {
		if w.seedWorkReady() {
			if w.fr.hitCount() >= w.fr.maxResults {
				w.cutProviderSeeds("budget_met")
			} else {
				if w.providerGraceStart.IsZero() {
					w.providerGraceStart = time.Now()
				}
				remaining := providerSeedGraceTimeout - time.Since(w.providerGraceStart)
				if remaining <= 0 && egressgate.From(ctx) == nil {
					w.cutProviderSeeds("grace")
				} else if remaining <= 0 && egressgate.From(ctx) != nil {
					select {
					case <-w.contribWake:
					case <-w.crawler.updates:
					case <-ctx.Done():
					case <-time.After(50 * time.Millisecond):
					}
					return true
				} else {
					grace := time.NewTimer(remaining)
					defer grace.Stop()
					select {
					case <-w.contribWake:
					case <-w.crawler.updates:
					case <-grace.C:
					case <-ctx.Done():
					}
					return true
				}
			}
		} else {
			remaining := providerSeedZeroHitTimeout - time.Since(w.providerSeedStart)
			if remaining <= 0 && egressgate.From(ctx) == nil {
				w.cutProviderSeeds("zero_hit_cap")
			} else if remaining <= 0 && egressgate.From(ctx) != nil {
				// Pending approval suspends the zero-hit cutoff.
				select {
				case <-w.contribWake:
				case <-w.crawler.updates:
				case <-ctx.Done():
				case <-time.After(50 * time.Millisecond):
				}
				return true
			} else {
				capTimer := time.NewTimer(remaining)
				defer capTimer.Stop()
				select {
				case <-w.contribWake:
				case <-w.crawler.updates:
				case <-capTimer.C:
				case <-ctx.Done():
				}
				return true
			}
		}
	}
	select {
	case <-w.contribWake:
	case <-w.crawler.updates:
	case <-ctx.Done():
	}
	return true
}
