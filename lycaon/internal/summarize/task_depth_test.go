package summarize

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/textrank"
)

func TestAnalyzeTermsCamelCase(t *testing.T) {
	got := termsToSet(textrank.Analyze("CreateSession", true, true))
	for _, want := range []string{"create", "session", "createsession"} {
		if !got[want] {
			t.Fatalf("Analyze(CreateSession)=%v, missing %q", got, want)
		}
	}
}

func termsToSet(terms []string) map[string]bool {
	out := make(map[string]bool, len(terms))
	for _, term := range terms {
		out[term] = true
	}
	return out
}

func TestChildTaskScoresNameBeatsPath(t *testing.T) {
	children := []*SubtreeNode{
		{Path: "pkg/sql_store.go", Kind: SubtreeKindFile, Material: Material{Defs: 10, SourceFiles: 1, Bytes: 1000}},
		{Path: "pkg/helpers.go", Kind: SubtreeKindFile, Material: Material{Defs: 10, SourceFiles: 1, Bytes: 1000}},
		{Path: "pkg/worker_cycle.go", Kind: SubtreeKindFile, Material: Material{Defs: 10, SourceFiles: 1, Bytes: 1000}},
	}
	index := childNameIndex{
		Names: map[string][]string{
			"pkg/sql_store.go":    {"CreateSession", "GetSession"},
			"pkg/helpers.go":      {"Fmt", "Clamp"},
			"pkg/worker_cycle.go": {"RunCycle", "Tick"},
		},
	}
	scores := childTaskScores("how does create and abort work?", children, index)
	if scores["pkg/sql_store.go"] <= scores["pkg/helpers.go"] {
		t.Fatalf("sql_store score=%v helpers=%v — CreateSession should win via name IDF",
			scores["pkg/sql_store.go"], scores["pkg/helpers.go"])
	}
}

func TestChildTaskScoresAbortSymbol(t *testing.T) {
	children := []*SubtreeNode{
		{Path: "pkg/a.go", Kind: SubtreeKindFile, Material: Material{Defs: 5, SourceFiles: 1}},
		{Path: "pkg/b.go", Kind: SubtreeKindFile, Material: Material{Defs: 5, SourceFiles: 1}},
	}
	index := childNameIndex{
		Names: map[string][]string{
			"pkg/a.go": {"Helper"},
			"pkg/b.go": {"AbortSession", "ForceCancel"},
		},
	}
	scores := childTaskScores("how does abort work?", children, index)
	if scores["pkg/b.go"] <= 0 || scores["pkg/b.go"] <= scores["pkg/a.go"] {
		t.Fatalf("scores=%v, want b.go (AbortSession) ahead", scores)
	}
}

func TestApplyImportPullThrough(t *testing.T) {
	parent := &SubtreeNode{
		Path: "pkg", Kind: SubtreeKindDir,
		Children: []*SubtreeNode{
			{Path: "pkg/store.go", Kind: SubtreeKindFile},
			{Path: "pkg/manager.go", Kind: SubtreeKindFile},
			{Path: "pkg/other.go", Kind: SubtreeKindFile},
		},
	}
	scores := map[string]float64{
		"pkg/store.go":   4,
		"pkg/manager.go": 0,
		"pkg/other.go":   0,
	}
	index := childNameIndex{}
	imports := []PackImportEdge{
		{From: "pkg/manager.go", To: "pkg/store.go", Kind: "outbound"},
	}
	scores = applyImportPullThrough(scores, parent, imports, index)
	if scores["pkg/manager.go"] <= 0 {
		t.Fatalf("manager should receive import pull-through; scores=%v", scores)
	}
	if scores["pkg/other.go"] != 0 {
		t.Fatalf("unrelated sibling must stay 0; scores=%v", scores)
	}
}

func TestSoftStarveWeights(t *testing.T) {
	children := []*SubtreeNode{
		{Path: "a"}, {Path: "b"}, {Path: "c"},
	}
	mat := []float64{0.33, 0.33, 0.34}
	scores := map[string]float64{"a": 0, "b": 5, "c": 0}
	w := softStarveWeights(children, mat, scores, 4)
	if w[1] <= w[0] || w[1] <= w[2] {
		t.Fatalf("weights=%v, want b (index 1) dominant", w)
	}
}

func TestRankChildrenTaskDepth(t *testing.T) {
	root := &SubtreeNode{
		Path: "pkg", Kind: SubtreeKindDir,
		Children: []*SubtreeNode{
			{Path: "pkg/a.go", Kind: SubtreeKindFile, Material: Material{Defs: 20, SourceFiles: 1}},
			{Path: "pkg/b.go", Kind: SubtreeKindFile, Material: Material{Defs: 5, SourceFiles: 1}},
		},
	}
	scores := map[string]float64{"pkg/a.go": 0, "pkg/b.go": 3}
	ranked := rankChildrenTaskDepth(root, nil, scores)
	if ranked[0].Path != "pkg/b.go" {
		t.Fatalf("ranked[0]=%s, want b.go despite less material", ranked[0].Path)
	}
}

func TestAllocateNameIndexTaskDepth(t *testing.T) {
	// Only mid.go matches through symbol names.
	children := []StructuralChild{
		{Path: "pkg/aaa.go", Kind: SubtreeKindFile, Defs: 12, SourceFiles: 1, Bytes: 1200},
		{Path: "pkg/mid.go", Kind: SubtreeKindFile, Defs: 12, SourceFiles: 1, Bytes: 1200},
		{Path: "pkg/zzz.go", Kind: SubtreeKindFile, Defs: 12, SourceFiles: 1, Bytes: 1200},
	}
	root := BuildSubtree(StructuralChild{
		Path: "pkg", Kind: SubtreeKindDir, Children: children,
	}, 6)
	structure := []StructureCandidate{
		fileCandWithSymbols("pkg/aaa.go", []string{"Helper", "Clamp"}),
		fileCandWithSymbols("pkg/mid.go", []string{"CreateSession", "AbortSession"}),
		fileCandWithSymbols("pkg/zzz.go", []string{"Fmt", "Log"}),
	}
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 4500
	pack, stats, _ := allocateContextPackForTest(
		"how does create and abort work?", root, structure, caps, FitEdges{}, nil)
	if stats.ChildrenDrilled < 1 {
		t.Fatalf("drilled=%d, want ≥1", stats.ChildrenDrilled)
	}
	if !packHasDepthOn(pack, "pkg/mid.go") {
		t.Fatalf("mid.go should have substance or non-rollup skeleton; drilled=%d boosts=%d substance=%v",
			stats.ChildrenDrilled, stats.TaskDepthBoosts, pack.Substance)
	}
}

func fileCandWithSymbols(path string, names []string) StructureCandidate {
	var syms []StructureSymbol
	var tags []string
	var b strings.Builder
	b.WriteString("package p\n")
	for i, n := range names {
		syms = append(syms, StructureSymbol{Kind: "func", Name: n, Line: i + 2})
		tags = append(tags, n)
		fmt.Fprintf(&b, "func %s() {}\n", n)
	}
	body := b.String()
	return StructureCandidate{
		RelPath: path, Kind: StructureKindFile, LineCount: strings.Count(body, "\n") + 1,
		Head: body, StartLine: 1, Symbols: syms, RollupRows: tags,
		ContentHash: HashString(body), Language: "go",
	}
}

func packHasDepthOn(pack ContextPack, path string) bool {
	for _, w := range pack.Substance {
		if w.Path == path {
			return true
		}
	}
	for _, s := range pack.Skeleton {
		if s.Path == path && s.Kind != KindDirectoryRollup {
			return true
		}
	}
	for _, id := range pack.Identity {
		if id.Path == path && id.Kind == StructureKindFile {
			return true
		}
	}
	return false
}
