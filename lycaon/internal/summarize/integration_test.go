package summarize

import (
	"context"
	"testing"
)

func TestIntegrationDocrefsExpansion(t *testing.T) {
	hub := Candidate{
		RelPath: "docs/hub.md", Kind: KindFile,
		ContentHash: HashString("hub"), Body: "hub",
	}
	linked := []Candidate{
		{RelPath: "docs/a.md", Kind: KindFile, ContentHash: HashString("a"), Body: "a"},
		{RelPath: "docs/b.md", Kind: KindFile, ContentHash: HashString("b"), Body: "b"},
	}
	cands := append([]Candidate{hub}, linked...)
	eng := NewEngine(fakeGather{GatherResult{
		Mode: ModeRepo, Candidates: cands,
		Stats: GatherStats{Mode: ModeRepo, Candidates: len(cands), PathIsFile: true},
	}}, DefaultCaps())

	res, err := eng.Run(context.Background(), Request{Task: "explain links", MaxAnchors: 12})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	paths := map[string]bool{}
	for _, s := range res.Sources {
		paths[s] = true
	}
	for _, want := range []string{"docs/a.md", "docs/b.md"} {
		if !paths[want] {
			t.Fatalf("missing source from expanded file %q; sources=%v", want, res.Sources)
		}
	}
}
