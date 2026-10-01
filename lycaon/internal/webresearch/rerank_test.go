package webresearch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
)

func TestVerifiedPagesBlendEngineRelevance(t *testing.T) {
	t.Parallel()
	fake := &decidetest.Fake{Scores: []float64{0, 4}}
	scorer := newQueryScorer("sqlite write-ahead log", nil, false, CurrentPeriod())
	scorer.rerank = decide.Reranker{Decider: fake, Policies: decide.Policies{decide.SiteWebPages: decide.RerankPolicy{
		Enabled: true, Deadline: time.Second, MaxCandidates: 8, Chunk: 8, Weight: 1,
	}}}
	survivors := []scoredVerifyHit{
		{candidate: indexCandidate{URL: "https://a.example/one"}, probe: pageProbe{title: "One", textSample: strings.Repeat("x", 600)}, content: 0.5},
		{candidate: indexCandidate{URL: "https://b.example/wal"}, probe: pageProbe{title: "WAL", headings: []string{"Checkpoints"}, description: "How the log works"}, content: 0.25},
	}
	rerankVerifyHits(context.Background(), scorer, survivors)
	if survivors[1].content != 1.25 || survivors[0].content != 0.5 {
		t.Fatalf("contents = %+v", []float64{survivors[0].content, survivors[1].content})
	}
	if len(fake.Ranks) != 1 || fake.Ranks[0].Task != "sqlite write-ahead log" {
		t.Fatalf("engine call = %+v", fake.Ranks)
	}
	texts := fake.Ranks[0].Candidates
	if !strings.HasPrefix(texts[1], "Page: https://b.example/wal\nTitle: WAL\nHeadings: Checkpoints\nDescription: How the log works") {
		t.Fatalf("page text = %q", texts[1])
	}
	if sample := strings.TrimPrefix(texts[0], "Page: https://a.example/one\nTitle: One\n"); len([]rune(sample)) != probeTextRunes {
		t.Fatalf("sample kept %d runes, want %d", len([]rune(sample)), probeTextRunes)
	}
}

func TestVerifiedPagesKeepContentOrderWithoutAnEngine(t *testing.T) {
	t.Parallel()
	scorer := newQueryScorer("q", nil, false, CurrentPeriod())
	survivors := []scoredVerifyHit{{content: 0.2}, {content: 0.9}}
	rerankVerifyHits(context.Background(), scorer, survivors)
	if survivors[0].content != 0.2 || survivors[1].content != 0.9 {
		t.Fatalf("contents changed without an engine: %+v", survivors)
	}
	rerankVerifyHits(context.Background(), nil, survivors)
}
