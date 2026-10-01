package search

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
)

func projectSearchPolicy(weight float64) decide.Policies {
	return decide.Policies{decide.SiteProjectSearch: decide.RerankPolicy{
		Enabled: true, Deadline: time.Second, MaxCandidates: 16, Chunk: 16, Weight: weight,
	}}
}

func TestRerankHitsBlendsEngineRelevanceOnTheLegScale(t *testing.T) {
	t.Parallel()
	fake := &decidetest.Fake{Scores: []float64{0, 4, 0}}
	// Scores and weight are exact binary fractions so the blend compares exactly.
	exec := &CodeExecutor{rerank: decide.Reranker{Decider: fake, Policies: projectSearchPolicy(0.25)}}
	hits := []Hit{
		{HitKind: HitKindCode, Path: "pkg/a.go", Line: 3, Snippet: "func Open()", Score: 0.5},
		{HitKind: HitKindCode, Path: "pkg/b.go", Line: 9, Snippet: "buf.Flush()", Score: 0.375},
		{HitKind: HitKindFile, Path: "pkg/flush.go", Score: 0.25},
	}
	exec.rerankHits(context.Background(), []string{"flush", ".+"}, hits)
	if hits[1].Score != 0.625 || hits[0].Score != 0.5 || hits[2].Score != 0.25 {
		t.Fatalf("scores = %v %v %v", hits[0].Score, hits[1].Score, hits[2].Score)
	}
	if len(fake.Ranks) != 1 || fake.Ranks[0].Task != "flush" {
		t.Fatalf("engine call = %+v", fake.Ranks)
	}
	if got := fake.Ranks[0].Candidates; got[0] != "File: pkg/a.go\nLine 3: func Open()" || got[2] != "File: pkg/flush.go" {
		t.Fatalf("candidate texts = %q", got)
	}
}

func TestRerankHitsLeavesScoresWithoutAnEngine(t *testing.T) {
	t.Parallel()
	exec := NewCodeExecutor(decide.Reranker{})
	hits := []Hit{{Path: "a", Score: 0.5}, {Path: "b", Score: 0.4}}
	exec.rerankHits(context.Background(), []string{"x"}, hits)
	if hits[0].Score != 0.5 || hits[1].Score != 0.4 {
		t.Fatalf("scores changed without an engine: %+v", hits)
	}
}
