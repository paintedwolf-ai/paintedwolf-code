package repomap

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
)

func pageOf(names ...string) []Tag {
	tags := make([]Tag, len(names))
	for i, name := range names {
		tags[i] = Tag{Kind: "function", Name: name, File: "pkg/" + name + ".go", Line: 1, Language: "go"}
	}
	return tags
}

func names(tags []Tag) []string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = t.Name
	}
	return out
}

func TestTagsPageKeepsFileOrderWithoutATask(t *testing.T) {
	tags := pageOf("Zeta", "Alpha", "Mid")
	sortTags(tags)
	orderTagsForTask(context.Background(), Options{}, tags)
	if got := names(tags); got[0] != "Alpha" || got[1] != "Mid" || got[2] != "Zeta" {
		t.Fatalf("order = %v", got)
	}
}

func TestTagsPageOrdersLexicallyThenByEngine(t *testing.T) {
	tags := pageOf("OpenSocket", "ParseHeader", "FlushBuffer")
	sortTags(tags)
	orderTagsForTask(context.Background(), Options{Task: "parse the header"}, tags)
	if got := names(tags); got[0] != "ParseHeader" {
		t.Fatalf("lexical order = %v", got)
	}
	fake := &decidetest.Fake{Scores: []float64{0, 4, 0}}
	rr := decide.Reranker{Decider: fake, Policies: decide.Policies{decide.SiteRepomapTags: decide.RerankPolicy{
		Enabled: true, Deadline: time.Second, MaxCandidates: 8, Chunk: 8, Weight: 5,
	}}}
	tags = pageOf("OpenSocket", "ParseHeader", "FlushBuffer")
	sortTags(tags)
	// The engine reads candidates in lexical order: ParseHeader first, then
	// the two zero-scored tags in file order. It rates the second one it
	// sees, FlushBuffer, a direct match, and the blend lifts it to the top.
	orderTagsForTask(context.Background(), Options{Task: "parse the header", Rerank: rr}, tags)
	if got := names(tags); got[0] != "FlushBuffer" || got[1] != "ParseHeader" {
		t.Fatalf("engine order = %v", got)
	}
	if len(fake.Ranks) != 1 || fake.Ranks[0].Candidates[1] != "File: pkg/FlushBuffer.go (go)\nSymbol: FlushBuffer (function)" {
		t.Fatalf("engine call = %+v", fake.Ranks)
	}
}

func TestTagTextCarriesTheSignatureLine(t *testing.T) {
	tag := Tag{Kind: "function", Name: "Parse", File: "p.go", Language: "go", Signature: "func Parse(b []byte) (Header, error) {"}
	if got := TagText(tag); got != "File: p.go (go)\nSymbol: Parse (function)\nfunc Parse(b []byte) (Header, error) {" {
		t.Fatalf("TagText = %q", got)
	}
	src := []byte("package p\n\nfunc Parse(b []byte) (Header, error) {\n\treturn Header{}, nil\n}\n")
	outcome := TagsFromBytes(context.Background(), "p.go", src)
	if len(outcome.Tags) != 1 || outcome.Tags[0].Signature != "func Parse(b []byte) (Header, error) {" {
		t.Fatalf("tags = %+v", outcome.Tags)
	}
}

func TestTagsPagingIsIndependentOfTheTask(t *testing.T) {
	snap := &Snapshot{MaxBytes: 1}
	all := pageOf("B", "A", "C")
	sortTags(all)
	assembleTagsView(snap, all, 1, 60)
	if len(snap.Tags) == 0 || snap.Tags[0].Name != "B" {
		t.Fatalf("page two must start at the second alphabetical tag: %+v", snap.Tags)
	}
}
