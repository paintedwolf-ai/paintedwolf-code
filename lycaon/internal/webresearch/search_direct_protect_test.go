package webresearch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Soft/bundled hits must not fanCancel Direct mid-flight.
func TestFanOutDoesNotCancelDirectOnSoftHits(t *testing.T) {
	prevGrace := fanOutGraceTimeout
	prevZero := fanOutZeroHitTimeout
	fanOutGraceTimeout = 50 * time.Millisecond
	fanOutZeroHitTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		fanOutGraceTimeout = prevGrace
		fanOutZeroHitTimeout = prevZero
	})

	var directCanceled atomic.Bool
	releaseDirect := make(chan struct{})
	fanCtx, fanCancel := context.WithCancel(context.Background())
	defer fanCancel()

	tasks := []fanTask{
		{
			direct: true,
			run: func() providerOutcome {
				select {
				case <-releaseDirect:
					return providerOutcome{
						providerID: directWireProviderID,
						ok:         true,
						hits: []WebHit{{
							URL:      "https://example.com/direct",
							Title:    "Direct hit",
							Provider: directWireProviderID,
						}},
					}
				case <-fanCtx.Done():
					directCanceled.Store(true)
					return providerOutcome{
						providerID: directWireProviderID,
						reason:     "cut",
						detail:     fanCtx.Err().Error(),
					}
				case <-time.After(30 * time.Second):
					return providerOutcome{providerID: directWireProviderID, reason: "timeout"}
				}
			},
		},
		{
			soft: true,
			run: func() providerOutcome {
				return providerOutcome{
					providerID: "arxiv",
					ok:         true,
					hits: []WebHit{{
						URL:      "https://arxiv.org/abs/1",
						Title:    "Soft hit",
						Provider: "arxiv",
					}},
				}
			},
		},
	}

	done := make(chan []providerOutcome, 1)
	go func() {
		done <- runProviderTasks(fanCtx, fanCancel, tasks, 10)
	}()

	if !directTaskPending(tasks, []bool{false, true}) {
		t.Fatal("unfinished Direct task must hold fan-out cancellation")
	}
	if directCanceled.Load() {
		t.Fatal("fan-out canceled Direct after soft/bundled hits")
	}
	close(releaseDirect)
	select {
	case outs := <-done:
		if len(outs) != 2 {
			t.Fatalf("outcomes = %d", len(outs))
		}
		if !outs[0].ok || outs[0].providerID != directWireProviderID {
			t.Fatalf("direct outcome = %+v", outs[0])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runProviderTasks hung")
	}
}

func TestFinalizeSearchResultSoftFillsOnlyWhenPrimaryEmpty(t *testing.T) {
	soft := map[string]struct{}{"arxiv": {}, "mdn": {}}
	directHit := WebHit{URL: "https://example.com/d", Title: "Direct", Provider: directWireProviderID}
	softHit := WebHit{
		URL:      "https://developer.mozilla.org/en-US/docs/Web/API/IDBIndex/openCursor",
		Title:    "Noise",
		Provider: "mdn",
	}

	withDirect := finalizeSearchResult("q", 10, []providerOutcome{
		{providerID: directWireProviderID, ok: true, hits: []WebHit{directHit}},
		{providerID: "mdn", ok: true, hits: []WebHit{softHit}},
	}, soft)
	if len(withDirect.Results) != 1 || withDirect.Results[0].Provider != directWireProviderID {
		t.Fatalf("with Direct hits = %+v want only Direct", withDirect.Results)
	}

	softOnly := finalizeSearchResult("q", 10, []providerOutcome{
		{providerID: directWireProviderID, ok: false, reason: "search_error"},
		{providerID: "mdn", ok: true, hits: []WebHit{softHit}},
	}, soft)
	if len(softOnly.Results) != 1 || softOnly.Results[0].Provider != "mdn" {
		t.Fatalf("Direct empty = %+v want soft fallback", softOnly.Results)
	}
}

func TestSeedWorkReadyIgnoresIndexMemoryAlone(t *testing.T) {
	fr := newFrontier(5, 2)
	fr.admit(indexCandidate{URL: "https://example.com/a", Title: "A", Source: sourceIndexMemory})
	w := &discoveryWait{fr: fr}
	if w.seedWorkReady() {
		t.Fatal("index_memory candidates alone must not start provider-seed grace")
	}
	fr.admit(indexCandidate{URL: "https://example.com/b", Title: "B", Source: sourceProviderSeed})
	if !w.seedWorkReady() {
		t.Fatal("provider_seed candidates must count as seed work")
	}
}

func TestHasProbeableMemoryRequiresPositiveScore(t *testing.T) {
	fr := newFrontier(5, 2)
	c := indexCandidate{
		URL:    "https://example.com/unrelated-cooking",
		Title:  "Unrelated cooking recipes",
		Source: sourceIndexMemory,
	}
	fr.admit(c)
	keys := map[string]struct{}{canonicalURL(c.URL): {}}
	scorer := newQueryScorer("local-first AI coding agent", nil, false, CurrentPeriod())
	if fr.hasProbeableMemory(keys, scorer) {
		t.Fatal("zero-score memory candidate must not count as probeable")
	}
}
