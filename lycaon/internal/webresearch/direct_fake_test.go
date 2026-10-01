package webresearch

import (
	"context"
)

// FakeDirectDiscoverer returns scripted hits for tests.
type FakeDirectDiscoverer struct {
	Hits  []WebHit
	Err   error
	Calls int
	// LastPeriod is the window of the most recent call.
	LastPeriod Period
}

func (f *FakeDirectDiscoverer) Search(ctx context.Context, req DirectRequest) ([]WebHit, error) {
	f.Calls++
	f.LastPeriod = req.Period
	maxResults := req.MaxResults
	if f.Err != nil {
		if stats := statsFrom(ctx); stats != nil {
			stats.setDirectOutcome(0, maxResults)
		}
		return nil, f.Err
	}
	hits := append([]WebHit(nil), f.Hits...)
	if maxResults > 0 && len(hits) > maxResults {
		hits = hits[:maxResults]
	}
	if stats := statsFrom(ctx); stats != nil {
		stats.setDirectOutcome(len(hits), maxResults)
	}
	return hits, nil
}
