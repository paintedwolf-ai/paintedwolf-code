package webresearch

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/webindex"
)

// memoryWireProviderID labels hits served from the persistent web index.
const memoryWireProviderID = "memory"

// sourceIndexMemory tags a candidate that came from the persistent index.
// Verification holds these to a higher relevance bar than live-crawl
// candidates (see relevanceFloor).
const sourceIndexMemory = "index_memory"

// fetchDescriptionLen bounds the text excerpt ingested for a fetched page.
const fetchDescriptionLen = 300

// searchIndexMemory serves the persistent web index as a search channel: FTS
// candidates are live-verified before they appear, and URLs that probe dead are
// evicted. It carries the whole result when every configured provider fails.
func searchIndexMemory(ctx context.Context, index *webindex.Store, rerank decide.Reranker, query string, period Period, maxResults int) providerOutcome {
	outcome := providerOutcome{providerID: memoryWireProviderID}
	if index == nil {
		outcome.reason = "not_configured"
		return outcome
	}
	docs, err := index.Search(ctx, query, maxResults*2+verifyOverfetch)
	if err != nil || len(docs) == 0 {
		outcome.reason = "no_results"
		return outcome
	}
	candidates := make([]indexCandidate, 0, len(docs))
	for _, doc := range docs {
		title := doc.Title
		if title == "" {
			title = doc.Anchors
		}
		candidates = append(candidates, indexCandidate{
			URL:    doc.URL,
			Title:  title,
			Notes:  strings.TrimSpace(strings.Join([]string{doc.Description, doc.Anchors}, " ")),
			Source: sourceIndexMemory,
			Date:   doc.Published,
		})
	}
	scorer := newQueryScorer(query, nil, false, period)
	scorer.rerank = rerank
	scorer.weighTerms(candidates)
	ranked := probeWorthy(rankCandidates(candidates, scorer), scorer)
	if len(ranked) == 0 {
		outcome.reason = "no_results"
		return outcome
	}
	if err := acquireDirectSlot(ctx); err != nil {
		outcome.reason = "timeout"
		return outcome
	}
	defer releaseDirectSlot()
	hits, _, dead := verifyHits(ctx, ranked, scorer, maxResults, maxHitsPerHost)
	for _, url := range dead {
		index.QueueDelete(ctx, url)
	}
	if len(hits) == 0 {
		outcome.reason = "no_results"
		return outcome
	}
	for i := range hits {
		hits[i].Provider = memoryWireProviderID
	}
	outcome.ok = true
	outcome.reason = "ok"
	outcome.hits = hits
	return outcome
}

// ingestProviderHits records custom provider results in the web index as
// unverified pages. Direct (already ingested as verified), memory, and soft
// bundled providers (keyword noise) are excluded.
func ingestProviderHits(ctx context.Context, index *webindex.Store, outcomes []providerOutcome, soft map[string]struct{}) {
	if index == nil {
		return
	}
	for _, o := range outcomes {
		if !o.ok || o.providerID == directWireProviderID || o.providerID == memoryWireProviderID {
			continue
		}
		if _, ok := soft[o.providerID]; ok {
			continue
		}
		for _, h := range o.hits {
			index.QueuePage(ctx, webindex.Page{
				URL:         h.URL,
				Title:       h.Title,
				Description: h.Snippet,
			})
		}
	}
}

// ingestFetchedPage records a page the agent explicitly opened — the
// strongest relevance signal available, from any provider flow.
func ingestFetchedPage(ctx context.Context, index *webindex.Store, res FetchURLResult) {
	if index == nil || res.Status < 200 || res.Status >= 300 {
		return
	}
	desc := strings.Join(strings.Fields(res.Text), " ")
	if len(desc) > fetchDescriptionLen {
		desc = desc[:fetchDescriptionLen]
	}
	index.QueuePage(ctx, webindex.Page{
		URL:         res.URL,
		Title:       res.Title,
		Description: desc,
	})
}
