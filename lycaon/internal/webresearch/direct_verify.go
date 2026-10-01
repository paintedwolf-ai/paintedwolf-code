package webresearch

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/observability"
)

const (
	// verifyBodyByteLimit bounds the per-page probe. Enough for <head> metadata
	// and the opening content of any real page; parked or spam pages fail the
	// relevance check within this window.
	verifyBodyByteLimit = 50 << 10
	verifyFetchTimeout  = 6 * time.Second
	verifyMaxParallel   = 5
	// verifyOverfetch is how many extra candidates are probed beyond
	// maxResults, so relevance-dropped pages don't shrink the result set and
	// the content re-rank has real choice.
	verifyOverfetch = 2
	// verifyTextSampleLen bounds the visible-text sample used for the
	// query-token relevance check and content scoring.
	verifyTextSampleLen = 4096
	maxProbeHeadings    = 8
	// Link-expansion harvest bounds. Anchors need wordy link text: content
	// links carry headlines, nav links carry one or two words.
	maxProbeLinks    = 16
	minLinkTextWords = 4
	maxExpandPool    = 24
	// thinTextSampleLen marks a live page whose fetched HTML carries almost no
	// readable text — a JS-rendered shell. The hit is annotated so agents skip
	// a fetch_url that would find the same.
	thinTextSampleLen = 280
)

// thinContentNote is appended to a hit whose page yielded no readable text.
const thinContentNote = "[page returned almost no readable text — likely rendered by JavaScript]"

// pageProbe is the outcome of one bounded GET during hit verification.
type pageProbe struct {
	live          bool
	robotsBlocked bool
	// inconclusive marks a probe that failed for a transient reason — timeout,
	// host cooldown, network error, 429/5xx. It is not evidence of a dead URL
	// and does not drive a delete from the persistent index.
	inconclusive bool
	title        string
	description  string
	date         time.Time
	dateFromPage bool
	headings     []string
	textSample   string
	links        []probeLink
	// mainContent reports that textSample/links came from readability's
	// article node rather than the whole-document fallback — the sample is
	// real page content, not site chrome.
	mainContent bool
}

// probeLink is an in-content anchor harvested during verification.
type probeLink struct {
	text string
	url  string
}

// probeThin reports whether a live page yielded effectively no readable text.
// A readability hit is never thin — short real content is still content.
func probeThin(p pageProbe) bool {
	return p.live && !p.mainContent && p.description == "" && len(p.headings) == 0 &&
		len(strings.TrimSpace(p.textSample)) < thinTextSampleLen
}

// probeTextRunes bounds the page sample the engine reads.
const probeTextRunes = 400

// rerankVerifyHits blends engine relevance into the survivors' content scores
// on the web_pages site. The lexical content score stays the base, so an
// abstaining engine leaves the order exactly as scored.
func rerankVerifyHits(ctx context.Context, scorer *queryScorer, survivors []scoredVerifyHit) {
	if scorer == nil || len(survivors) < 2 || !scorer.rerank.Active(decide.SiteWebPages) {
		return
	}
	lexical := make([]float64, len(survivors))
	texts := make([]string, len(survivors))
	for i, s := range survivors {
		lexical[i] = s.content
		texts[i] = probeText(s.candidate, s.probe)
	}
	blended, _ := scorer.rerank.Rerank(ctx, decide.SiteWebPages, scorer.query, lexical, texts)
	for i := range survivors {
		survivors[i].content = blended[i]
	}
}

// probeText is what the engine reads for one verified page.
func probeText(c indexCandidate, p pageProbe) string {
	var b strings.Builder
	b.WriteString("Page: ")
	b.WriteString(c.URL)
	if title := strings.TrimSpace(p.title); title != "" {
		b.WriteString("\nTitle: ")
		b.WriteString(title)
	}
	if len(p.headings) > 0 {
		b.WriteString("\nHeadings: ")
		b.WriteString(strings.Join(p.headings, " | "))
	}
	if desc := strings.TrimSpace(p.description); desc != "" {
		b.WriteString("\nDescription: ")
		b.WriteString(desc)
	}
	if sample := []rune(strings.TrimSpace(p.textSample)); len(sample) > 0 {
		b.WriteString("\n")
		b.WriteString(string(sample[:min(len(sample), probeTextRunes)]))
	}
	return b.String()
}

// verifyHits probes candidates in rank order and returns confirmed hits plus
// link-expansion candidates. A hit is live and query-relevant (parked domains
// answer 200 to anything); survivors re-rank by fetched content, then cap per
// host. perHostCap <= 0 disables the cap; capped-out hits are dropped, not
// deferred.
func verifyHits(ctx context.Context, picks []indexCandidate, scorer *queryScorer, maxResults, perHostCap int) ([]WebHit, []indexCandidate, []string) {
	if maxResults <= 0 || len(picks) == 0 {
		return nil, nil, nil
	}
	budget := maxResults + verifyOverfetch
	if budget > len(picks) {
		budget = len(picks)
	}
	stats := statsFrom(ctx)
	probes := make([]pageProbe, budget)
	probeMs := make([]int64, budget)
	sem := make(chan struct{}, verifyMaxParallel)
	var wg sync.WaitGroup
	for i := 0; i < budget; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			probeStart := time.Now()
			probes[idx] = fetchPageProbe(ctx, picks[idx].URL)
			elapsed := time.Since(probeStart)
			probeMs[idx] = elapsed.Milliseconds()
			stats.addProbe(elapsed)
		}(i)
	}
	wg.Wait()

	var dead []string
	survivors := make([]scoredVerifyHit, 0, budget)
	for i := 0; i < budget; i++ {
		c, probe := picks[i], probes[i]
		verdict := "verified"
		switch {
		case !probe.live && ctx.Err() != nil:
			verdict = "aborted"
		case !probe.live && probe.robotsBlocked && apiSnippetFallbackEligible(c):
			verdict = "api_snippet"
		case !probe.live && probe.inconclusive:
			verdict = "unreachable"
		case !probe.live:
			verdict = "dead"
		case !probeRelevant(probe, c, scorer):
			verdict = "irrelevant"
		case probeThin(probe):
			verdict = "thin"
		}
		observability.LogWebSearchFetch(observability.WebSearchFetchCapture{
			SearchID: stats.id(),
			Phase:    stats.currentPhase(),
			Kind:     "probe",
			URL:      c.URL,
			Host:     strings.ToLower(urlHost(c.URL)),
			Source:   c.Source,
			FetchMs:  probeMs[i],
			Verdict:  verdict,
		})
		if !probe.live {
			if ctx.Err() == nil && verdict == "api_snippet" {
				fallback := pageProbe{
					live:        true,
					title:       strings.TrimSpace(c.Title),
					description: strings.TrimSpace(c.APISnippet),
					mainContent: true,
				}
				survivors = append(survivors, scoredVerifyHit{
					candidate: c,
					probe:     fallback,
					content:   scorer.scoreProbe(fallback) - scorer.seenPenaltyFor(c.URL),
				})
				continue
			}
			// Only a completed probe that found nothing there is evidence the page
			// is gone; deadlines, cooldowns, and 503s leave the index alone.
			if ctx.Err() == nil && verdict == "dead" {
				dead = append(dead, c.URL)
			}
			continue
		}
		if verdict == "irrelevant" {
			continue
		}
		survivors = append(survivors, scoredVerifyHit{
			candidate: c,
			probe:     probe,
			content:   scorer.scoreProbe(probe) - scorer.seenPenaltyFor(c.URL),
		})
	}
	rerankVerifyHits(ctx, scorer, survivors)
	// Content score first; crawl-rank order (input order) breaks ties, which
	// keeps recency ordering for pages whose fetched content ties (e.g. empty
	// bodies behind JS).
	sort.SliceStable(survivors, func(i, j int) bool {
		return survivors[i].content > survivors[j].content
	})

	out := selectVerifyHits(survivors, maxResults, perHostCap, scorer)
	harvest := make([]pageProbe, 0, len(survivors))
	for _, s := range survivors {
		harvest = append(harvest, s.probe)
	}
	return out, harvestExpandCandidates(picks, harvest, scorer), dead
}
