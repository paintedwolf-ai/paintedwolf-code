package webresearch

import (
	"strings"
	"time"
)

// planPageCandidates turns LLM-proposed path-bearing seed URLs into frontier
// candidates whose anchors are harvested when probed. Leads carry hosts and
// headline words only.
func planPageCandidates(plan seedPlan) []indexCandidate {
	var out []indexCandidate
	for _, seed := range plan.seeds {
		if page := pageSeedURL(seed); page != "" {
			out = append(out, indexCandidate{URL: page, Title: sitemapTitle(page), Source: "llm_seed"})
		}
	}
	return out
}

// mergeHits appends extra hits with canonical-URL dedup, re-enforcing the
// total and per-host bounds across the combined set.
func mergeHits(hits, extra []WebHit, maxResults, perHostCap int) []WebHit {
	seen := make(map[string]struct{}, len(hits))
	perHost := make(map[string]int, len(hits))
	for _, h := range hits {
		seen[canonicalURL(h.URL)] = struct{}{}
		perHost[strings.ToLower(urlHost(h.URL))]++
	}
	for _, h := range extra {
		if len(hits) >= maxResults {
			break
		}
		key := canonicalURL(h.URL)
		if _, ok := seen[key]; ok {
			continue
		}
		host := strings.ToLower(urlHost(h.URL))
		if perHostCap > 0 && perHost[host] >= perHostCap {
			continue
		}
		seen[key] = struct{}{}
		perHost[host]++
		hits = append(hits, h)
	}
	return hits
}

// seedPlan is the parsed seed-call response.
type seedPlan struct {
	seeds  []string
	fresh  bool
	expand []string
	leads  []seedLead
}

// freshFor resolves the recency preference. The seed model judges whether
// recency matters for the subject; a declared past window overrides it.
func (p seedPlan) freshFor(period Period) bool {
	if period.Historical(time.Now()) {
		return false
	}
	return p.fresh
}

// rankPhrases returns lowercase phrases used for match scoring and the
// verification relevance gate. Phrases come from the query, LLM expand lines,
// and lead titles — not host-side tokenization, so any language the model
// supplies works.
func (p seedPlan) rankPhrases(query string) []string {
	var out []string
	seen := make(map[string]struct{})
	add := func(phrase string) {
		phrase = strings.TrimSpace(strings.ToLower(phrase))
		if len(phrase) < 2 {
			return
		}
		if _, ok := seen[phrase]; ok {
			return
		}
		seen[phrase] = struct{}{}
		out = append(out, phrase)
	}
	add(query)
	for _, phrase := range quotedPhrases(query) {
		add(phrase)
	}
	for _, phrase := range p.expand {
		add(phrase)
	}
	for _, lead := range p.leads {
		add(lead.title)
	}
	return out
}

// crawlPhrases is rankPhrases plus lead-headline n-grams: loose matching for
// discovery, since whole invented headlines rarely match live URLs and every
// candidate still passes strict verification.
func (p seedPlan) crawlPhrases(query string) []string {
	out := p.rankPhrases(query)
	seen := make(map[string]struct{}, len(out))
	for _, phrase := range out {
		seen[phrase] = struct{}{}
	}
	for _, lead := range p.leads {
		for _, gram := range leadTitleNGrams(lead.title) {
			if _, ok := seen[gram]; ok {
				continue
			}
			seen[gram] = struct{}{}
			out = append(out, gram)
		}
	}
	return out
}

// hostBases returns the site roots the plan names — seeds plus lead hosts.
func (p seedPlan) hostBases() []string {
	var out []string
	seen := make(map[string]struct{})
	add := func(base string) {
		if base == "" {
			return
		}
		key := strings.ToLower(base)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, base)
	}
	for _, seed := range p.seeds {
		add(normalizeSiteBase(seed))
	}
	for _, lead := range p.leads {
		add(leadPublisherBase(lead))
	}
	return out
}
