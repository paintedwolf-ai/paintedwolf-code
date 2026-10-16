package main

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Without an engine the eval still measures the site, enabled and with the
// command line's overrides applied over the shipped catalog.
func TestRerankerWithoutEngineAppliesOverridesToTheCatalogPolicy(t *testing.T) {
	catalog, err := decide.LoadCatalog()
	testutil.FailErr(t, "load catalog", err)
	var site decide.Site
	for s := range catalog.Policies {
		site = s
		break
	}
	if site == "" {
		t.Fatal("the shipped catalog declares no rerank sites")
	}
	cfg := evalConfig{site: site, noEngine: true, weight: 0.25, k: 7, chunk: 3, deadline: 2 * time.Second}

	rr, engine, done, err := reranker(t.Context(), cfg)
	testutil.FailErr(t, "reranker", err)
	defer done()
	if rr.Decider != nil || engine != (decide.Engine{}) {
		t.Fatalf("no-engine reranker has decider %v and engine %+v", rr.Decider, engine)
	}
	got := rr.Policies.For(site)
	if !got.Enabled || got.Weight != 0.25 || got.MaxCandidates != 7 || got.Chunk != 3 || got.Deadline != 2*time.Second {
		t.Fatalf("policy for %s = %+v, want enabled with the overrides", site, got)
	}
}
