package decide_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
)

func enabled(k, chunk int, weight float64) decide.Policies {
	return decide.Policies{decide.SiteRepomapTags: decide.RerankPolicy{
		Enabled: true, Deadline: time.Second, MaxCandidates: k, Chunk: chunk, Weight: weight,
	}}
}

// chunkFake answers Rank per chunk from a scripted table keyed by candidate text.
type chunkFake struct {
	decidetest.Fake
	byText map[string]float64
}

func (f *chunkFake) Rank(ctx context.Context, head decide.Head, task string, candidates []string) ([]float64, decide.Engine, error) {
	if _, _, err := f.Fake.Rank(ctx, head, task, candidates); err != nil {
		return nil, decide.Engine{}, err
	}
	out := make([]float64, len(candidates))
	for i, c := range candidates {
		out[i] = f.byText[c]
	}
	return out, decide.Engine{Name: "fake", Model: "scripted"}, nil
}

func TestRerankBlendsTheLexicalTopKInChunks(t *testing.T) {
	t.Parallel()
	fake := &chunkFake{byText: map[string]float64{"a": 4, "b": 0, "c": 2, "d": 4}}
	rr := decide.Reranker{Decider: fake, Policies: enabled(3, 2, 1)}
	lexical := []float64{0.1, 0.9, 0.5, 0.0}
	got, outcome := rr.Rerank(context.Background(), decide.SiteRepomapTags, "find it", lexical, []string{"a", "b", "c", "d"})
	if outcome.Abstained || outcome.Scored != 3 || outcome.Candidates != 4 {
		t.Fatalf("outcome = %+v", outcome)
	}
	// Top three by lexical are b, c, a; d never reaches the engine.
	want := []float64{0.1 + 1, 0.9 + 0, 0.5 + 0.5, 0.0}
	if !slices.Equal(got, want) {
		t.Fatalf("blended = %v want %v", got, want)
	}
	if len(fake.Ranks) != 2 || len(fake.Ranks[0].Candidates) != 2 || len(fake.Ranks[1].Candidates) != 1 {
		t.Fatalf("chunks = %+v", fake.Ranks)
	}
	if !slices.Equal(fake.Ranks[0].Candidates, []string{"b", "c"}) {
		t.Fatalf("first chunk must carry the lexical leaders: %v", fake.Ranks[0].Candidates)
	}
	if &got[0] == &lexical[0] {
		t.Fatal("result must not alias the input")
	}
}

func TestRerankAbstainsWithoutChangingOrder(t *testing.T) {
	t.Parallel()
	lexical := []float64{0.3, 0.2}
	texts := []string{"x", "y"}
	cases := []struct {
		name string
		rr   decide.Reranker
		task string
		want string
	}{
		{"disabled", decide.Reranker{Decider: &decidetest.Fake{}, Policies: nil}, "t", "site disabled"},
		{"no engine", decide.Reranker{Policies: enabled(2, 2, 1)}, "t", "engine unavailable"},
		{"unavailable", decide.Reranker{Decider: &decidetest.Fake{Unavailable: true}, Policies: enabled(2, 2, 1)}, "t", "engine unavailable"},
		{"no task", decide.Reranker{Decider: &decidetest.Fake{}, Policies: enabled(2, 2, 1)}, "  ", "no task"},
		{"fault", decide.Reranker{Decider: &decidetest.Fake{Err: errors.New("boom")}, Policies: enabled(2, 2, 1)}, "t", "engine fault: boom"},
	}
	for _, tc := range cases {
		got, outcome := tc.rr.Rerank(context.Background(), decide.SiteRepomapTags, tc.task, lexical, texts)
		if !outcome.Abstained || outcome.Reason != tc.want {
			t.Fatalf("%s: outcome = %+v", tc.name, outcome)
		}
		if !slices.Equal(got, lexical) {
			t.Fatalf("%s: abstention changed scores: %v", tc.name, got)
		}
	}
	single, outcome := (decide.Reranker{Decider: &decidetest.Fake{}, Policies: enabled(2, 2, 1)}).Rerank(context.Background(), decide.SiteRepomapTags, "t", []float64{1}, []string{"only"})
	if !outcome.Abstained || outcome.Reason != "nothing to rank" || len(single) != 1 {
		t.Fatalf("single candidate outcome = %+v", outcome)
	}
}

// stallFake never answers, standing in for a warming engine.
type stallFake struct{ decidetest.Fake }

func (*stallFake) Rank(ctx context.Context, _ decide.Head, _ string, _ []string) ([]float64, decide.Engine, error) {
	<-ctx.Done()
	return nil, decide.Engine{}, ctx.Err()
}

func TestRerankHonoursTheDeadline(t *testing.T) {
	t.Parallel()
	policies := decide.Policies{decide.SiteRepomapTags: decide.RerankPolicy{Enabled: true, Deadline: 30 * time.Millisecond, MaxCandidates: 4, Chunk: 4, Weight: 1}}
	rr := decide.Reranker{Decider: &stallFake{}, Policies: policies}
	lexical := []float64{0.5, 0.4}
	started := time.Now()
	got, outcome := rr.Rerank(context.Background(), decide.SiteRepomapTags, "t", lexical, []string{"a", "b"})
	if time.Since(started) > time.Second {
		t.Fatal("rerank waited past its deadline")
	}
	if !outcome.Abstained || outcome.Reason != "deadline exceeded" || !slices.Equal(got, lexical) {
		t.Fatalf("outcome = %+v scores = %v", outcome, got)
	}
}

func TestRerankObservesEveryCall(t *testing.T) {
	t.Parallel()
	var seen []decide.Observation
	rr := decide.Reranker{Decider: &decidetest.Fake{Scores: []float64{4, 2}}, Policies: enabled(2, 2, 2), Observe: func(o decide.Observation) { seen = append(seen, o) }}
	rr.Rerank(context.Background(), decide.SiteRepomapTags, "t", []float64{0, 0}, []string{"p", "q"})
	rr.Rerank(context.Background(), decide.SiteRepomapTags, "", []float64{0, 0}, []string{"p", "q"})
	if len(seen) != 2 {
		t.Fatalf("observations = %d", len(seen))
	}
	if seen[0].Unit[0] != 1 || seen[0].Unit[1] != 0.5 || seen[0].Blended[0] != 2 || seen[0].Blended[1] != 1 {
		t.Fatalf("scored observation = %+v", seen[0])
	}
	if !seen[1].Outcome.Abstained || seen[1].Unit != nil {
		t.Fatalf("abstained observation = %+v", seen[1])
	}
	if !rr.Active(decide.SiteRepomapTags) || rr.Active(decide.SiteWebPages) {
		t.Fatal("Active must follow the site policy")
	}
}

func TestObserverAloneKeepsASiteActiveAndAbstains(t *testing.T) {
	t.Parallel()
	var seen []decide.Observation
	rr := decide.Reranker{Policies: enabled(2, 2, 1), Observe: func(o decide.Observation) { seen = append(seen, o) }}
	if !rr.Active(decide.SiteRepomapTags) {
		t.Fatal("an observer must see what the site offers even without an engine")
	}
	got, outcome := rr.Rerank(context.Background(), decide.SiteRepomapTags, "t", []float64{0.2, 0.1}, []string{"a", "b"})
	if !outcome.Abstained || outcome.Reason != "engine unavailable" || !slices.Equal(got, []float64{0.2, 0.1}) {
		t.Fatalf("outcome = %+v scores = %v", outcome, got)
	}
	if len(seen) != 1 || len(seen[0].Texts) != 2 {
		t.Fatalf("observation = %+v", seen)
	}
	if (decide.Reranker{Policies: enabled(2, 2, 1)}).Active(decide.SiteRepomapTags) {
		t.Fatal("without an engine or an observer a site must stay inactive")
	}
}
