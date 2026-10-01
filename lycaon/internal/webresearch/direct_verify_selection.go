package webresearch

import (
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

type scoredVerifyHit struct {
	candidate indexCandidate
	probe     pageProbe
	content   float64
}

// selectVerifyHits fills results SERP-style: one strong hit per host first, then
// backfills by content score up to per-host caps.
func selectVerifyHits(survivors []scoredVerifyHit, maxResults, perHostCap int, scorer *queryScorer) []WebHit {
	if len(survivors) == 0 || maxResults <= 0 {
		return nil
	}
	perHost := make(map[string]int)
	chosen := make([]bool, len(survivors))
	out := make([]WebHit, 0, maxResults)

	take := func(i int) {
		s := survivors[i]
		host := strings.ToLower(urlHost(s.candidate.URL))
		if perHostCap > 0 && perHost[host] >= perHostCap {
			return
		}
		chosen[i] = true
		perHost[host]++
		out = append(out, hitFromProbe(s.candidate, s.probe, scorer))
	}

	seenHost := make(map[string]struct{})
	for i := range survivors {
		if len(out) >= maxResults {
			break
		}
		host := strings.ToLower(urlHost(survivors[i].candidate.URL))
		if _, ok := seenHost[host]; ok {
			continue
		}
		seenHost[host] = struct{}{}
		take(i)
	}
	for i := range survivors {
		if len(out) >= maxResults {
			break
		}
		if chosen[i] {
			continue
		}
		take(i)
	}
	return out
}

// harvestExpandCandidates turns in-content anchors from verified pages into
// second-hop candidates, reaching hosts the seed model couldn't name. Anchors
// lexically match the query; "link_expand" provenance makes verification demand
// content-level relevance.
func harvestExpandCandidates(picks []indexCandidate, probes []pageProbe, scorer *queryScorer) []indexCandidate {
	if len(probes) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(picks))
	for _, p := range picks {
		seen[canonicalURL(p.URL)] = struct{}{}
	}
	var out []indexCandidate
	for _, probe := range probes {
		for _, link := range probe.links {
			if len(out) >= maxExpandPool {
				return out
			}
			key := canonicalURL(link.url)
			if _, ok := seen[key]; ok {
				continue
			}
			if scorer.coverage(strings.ToLower(link.text)) <= 0 && !scorer.anyPhraseIn(link.text) {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, indexCandidate{Title: link.text, URL: link.url, Source: "link_expand"})
		}
	}
	return out
}

// probeRelevant decides whether a live page is about the query. Model-named
// candidates (llm_seed/llm_lead) match on fetched content only, since their
// titles are invented. A page with no fetched text passes as inconclusive.
func probeRelevant(probe pageProbe, c indexCandidate, scorer *queryScorer) bool {
	fetchedText := strings.ToLower(strings.TrimSpace(probe.title) + " " + strings.TrimSpace(probe.description) + " " +
		strings.Join(probe.headings, " ") + " " + probe.textSample)
	metaText := strings.ToLower(strings.TrimSpace(c.Title) + " " + strings.TrimSpace(c.Notes) + " " +
		strings.ToLower(c.URL))
	pageText := fetchedText
	switch c.Source {
	case "llm_seed", "llm_lead":
	default:
		pageText = strings.TrimSpace(metaText + " " + fetchedText)
	}
	matched := false
	if scorer.anyPhraseIn(pageText) {
		matched = true
	} else if scorer.primaryMatches(pageText) >= relevanceFloor(c, scorer) {
		matched = true
	}
	if !matched {
		// Remembered pages get no inconclusive-fetch pass: an empty-provider
		// run would otherwise surface an unrelated JS shell.
		if c.Source == sourceIndexMemory {
			return false
		}
		return strings.TrimSpace(fetchedText) == ""
	}
	return !dateOutsideFreshWindow(c, probe, scorer)
}

// relevanceFloor is the minimum number of primary query terms a live page
// matches to count as relevant. Remembered pages clear a higher,
// query-length-scaled bar because memory answers when providers come back empty.
func relevanceFloor(c indexCandidate, scorer *queryScorer) int {
	need := probePrimaryFloor(scorer.meaningfulPrimary())
	if scorer.fresh && scorer.meaningfulPrimary() >= 3 && need < 2 {
		need = 2
	}
	if c.Source == sourceIndexMemory {
		if floor := memoryPrimaryFloor(scorer.meaningfulPrimary()); floor > need {
			need = floor
		}
	}
	return need
}

// memoryPrimaryFloor scales the memory relevance bar to query length so
// generic-token overlap on a long query does not pass.
func memoryPrimaryFloor(primary int) int {
	if primary <= 2 {
		return primary
	}
	if primary >= 7 {
		return 3
	}
	return 2
}

// dateOutsideFreshWindow uses provenance-bearing dates only.
func dateOutsideFreshWindow(c indexCandidate, probe pageProbe, scorer *queryScorer) bool {
	if !scorer.fresh {
		return false
	}
	anchorYear := scorer.period.Newest()
	if anchorYear == 0 {
		anchorYear = scorer.now.Year()
	}
	if anchorYear < 2010 {
		return false
	}
	date := probe.date
	if date.IsZero() {
		date = c.Date
	}
	return !date.IsZero() && date.Year() < anchorYear-1
}

func apiSnippetFallbackEligible(c indexCandidate) bool {
	return c.Source == sourceProviderSeed &&
		strings.TrimSpace(c.APISnippet) != "" &&
		strings.TrimSpace(c.Title) != ""
}

func hitFromProbe(c indexCandidate, probe pageProbe, scorer *queryScorer) WebHit {
	title := strings.TrimSpace(probe.title)
	if title == "" {
		title = strings.TrimSpace(c.Title)
	}
	if title == "" {
		title = sitemapTitle(c.URL)
	}
	date := probe.date
	if date.IsZero() {
		date = c.Date
	}
	provider := directWireProviderID
	if id := strings.TrimSpace(c.APIProviderID); id != "" {
		provider = id
	}
	hit := WebHit{
		Title:    title,
		URL:      c.URL,
		Snippet:  probeSnippet(c, probe, scorer),
		Provider: provider,
	}
	if !date.IsZero() {
		hit.Date = date.Format(webHitDateLayout)
	}
	return hit
}

// probeSnippet prefers the page's own description, then index notes, then the
// sample sentence that best matches the query. Pages with no readable text
// carry thinContentNote.
func probeSnippet(c indexCandidate, probe pageProbe, scorer *queryScorer) string {
	body := webindex.NormalizeWebTextForStorage(strings.TrimSpace(probe.description), 0)
	if body == "" {
		body = webindex.NormalizeWebTextForStorage(strings.TrimSpace(c.Notes), 0)
	}
	if body == "" {
		body = webindex.NormalizeWebTextForStorage(scorer.bestSentence(probe.textSample), 0)
	}
	if body == "" {
		body = webindex.NormalizeWebTextForStorage(strings.TrimSpace(probe.textSample), 0)
	}
	if body == "" {
		body = webindex.NormalizeWebTextForStorage(strings.TrimSpace(c.Title), 0)
	}
	if len(body) > maxSnippetLen {
		body = body[:maxSnippetLen]
	}
	if probeThin(probe) {
		if body == "" {
			body = thinContentNote
		} else {
			body += " " + thinContentNote
		}
	}
	return body
}
