package summarize

import (
	"reflect"
	"strings"
	"testing"
)

func equalMaterialSiblings() (*SubtreeNode, []StructureCandidate) {
	root := &SubtreeNode{
		Path: "root", Kind: SubtreeKindDir,
		Children: []*SubtreeNode{
			{
				Path: "root/alpha", Kind: SubtreeKindDir,
				Material: Material{Defs: 10, SourceFiles: 1, Bytes: 1000},
				Children: []*SubtreeNode{{
					Path: "root/alpha/a.go", Kind: SubtreeKindFile,
					Material: Material{Defs: 10, SourceFiles: 1, Bytes: 1000},
				}},
			},
			{
				Path: "root/beta", Kind: SubtreeKindDir,
				Material: Material{Defs: 10, SourceFiles: 1, Bytes: 1000},
				Children: []*SubtreeNode{{
					Path: "root/beta/b.go", Kind: SubtreeKindFile,
					Material: Material{Defs: 10, SourceFiles: 1, Bytes: 1000},
				}},
			},
		},
		Material: Material{Defs: 20, SourceFiles: 2, Bytes: 2000},
	}
	structure := []StructureCandidate{
		fileCand("root/alpha/a.go", 10, "go"),
		fileCand("root/beta/b.go", 10, "go"),
	}
	return root, structure
}

func TestRankChildrenTaskPathAffinity(t *testing.T) {
	root := &SubtreeNode{
		Path: "pkg", Kind: SubtreeKindDir,
		Children: []*SubtreeNode{
			{Path: "pkg/sql_store.go", Kind: SubtreeKindFile, Material: Material{Defs: 10, SourceFiles: 1, Bytes: 1000}},
			{Path: "pkg/abort_impl.go", Kind: SubtreeKindFile, Material: Material{Defs: 10, SourceFiles: 1, Bytes: 1000}},
			{Path: "pkg/worker_cycle.go", Kind: SubtreeKindFile, Material: Material{Defs: 10, SourceFiles: 1, Bytes: 1000}},
		},
	}
	ranked := rankChildrenImportance(root, nil, "how does abort work?")
	if len(ranked) < 1 || ranked[0].Path != "pkg/abort_impl.go" {
		t.Fatalf("ranked[0]=%v, want abort_impl.go first via path IDF", pathsOf(ranked))
	}
}

func pathsOf(nodes []*SubtreeNode) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n != nil {
			out = append(out, n.Path)
		}
	}
	return out
}

func TestRankChildrenImportanceTiebreak(t *testing.T) {
	root, _ := equalMaterialSiblings()
	ranked := rankChildren(root)
	if ranked[0].Path != "root/alpha" {
		t.Fatalf("material-only first = %s, want root/alpha", ranked[0].Path)
	}
	imp := SubtreeImportance{"root/beta": {DocLinks: 3}}
	ranked = rankChildrenImportance(root, imp, "")
	if ranked[0].Path != "root/beta" {
		t.Fatalf("doc-link first = %s, want root/beta", ranked[0].Path)
	}
}

func TestDocLinkWinsDepthTie(t *testing.T) {
	root, structure := equalMaterialSiblings()
	caps := DefaultCaps()
	caps.Pack.SubtreeFanoutMax = 1
	caps.Pack.SubtreeDoclinkMax = 32
	caps.Pack.SubtreeFaninMax = 0
	caps.Pack.InputBudgetTokens = 4000

	imp := SubtreeImportance{"root/beta": {DocLinks: 5}}
	pack, stats, _ := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, imp)

	if !packMentionsPath(pack, "root/beta") {
		t.Fatalf("doc-linked beta absent; skeleton=%v stats=%+v", pack.Skeleton, stats)
	}
	betaDrilled := false
	for _, id := range pack.Identity {
		if id.Path == "root/beta" || strings.HasPrefix(id.Path, "root/beta/") {
			betaDrilled = true
		}
	}
	for _, s := range pack.Skeleton {
		if (s.Path == "root/beta" || strings.HasPrefix(s.Path, "root/beta/")) && s.Kind != KindDirectoryRollup {
			betaDrilled = true
		}
	}
	if !betaDrilled && stats.ChildrenDrilled == 0 {
		t.Fatalf("expected beta drilled via doc-link fanout win; stats=%+v pack=%+v", stats, pack.Skeleton)
	}
	// Alpha should be in the fanout tail rollup, not drilled as primary.
	for _, s := range pack.Skeleton {
		if strings.HasPrefix(s.Path, "+") && s.Kind == KindDirectoryRollup {
			return
		}
	}
	t.Fatal("expected +N more fanout rollup for the unlinked sibling")
}

func TestFanInWinsDepthTie(t *testing.T) {
	root, structure := equalMaterialSiblings()
	caps := DefaultCaps()
	caps.Pack.SubtreeFanoutMax = 1
	caps.Pack.SubtreeFaninMax = 64
	caps.Pack.SubtreeDoclinkMax = 0
	caps.Pack.InputBudgetTokens = 4000

	imp := SubtreeImportance{"root/beta": {FanIn: 9}}
	pack, stats, _ := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, imp)
	if !packMentionsPath(pack, "root/beta") {
		t.Fatalf("fan-in beta absent; stats=%+v", stats)
	}
	hasTail := false
	for _, s := range pack.Skeleton {
		if strings.HasPrefix(s.Path, "+") {
			hasTail = true
		}
	}
	if !hasTail {
		t.Fatal("expected fanout tail for low-fan-in sibling")
	}
}

func TestSignalsDisabledParity(t *testing.T) {
	root, structure := equalMaterialSiblings()
	imp := SubtreeImportance{"root/beta": {DocLinks: 9, FanIn: 9}}
	caps := DefaultCaps()
	caps.Pack.SubtreeDoclinkMax = 0
	caps.Pack.SubtreeFaninMax = 0
	caps.Pack.SubtreeFanoutMax = 1
	caps.Pack.InputBudgetTokens = 800

	packOff, statsOff, _ := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, imp)
	packNil, _, _ := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, nil)
	if !reflect.DeepEqual(packOff, packNil) {
		t.Fatal("caps=0 must match the nil-importance pack")
	}
	if statsOff.DoclinkBoosts != 0 || statsOff.FaninBoosts != 0 {
		t.Fatalf("boosts must be 0 when disabled: %+v", statsOff)
	}
}

func TestSignalsDoNotExpandPile(t *testing.T) {
	root, structure := equalMaterialSiblings()
	imp := SubtreeImportance{
		"root/beta":  {DocLinks: 5},
		"root/ghost": {DocLinks: 99},
	}
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 800
	pack, _, _ := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, imp)
	if packMentionsPath(pack, "root/ghost") {
		t.Fatal("importance must not admit paths outside the subtree walk")
	}
	for _, id := range pack.Identity {
		if strings.Contains(id.Path, "ghost") {
			t.Fatalf("ghost identity: %s", id.Path)
		}
	}
}

func TestSignalsMetrics(t *testing.T) {
	root, structure := equalMaterialSiblings()
	imp := SubtreeImportance{
		"root/beta":  {DocLinks: 5},
		"root/alpha": {DocLinks: 0},
	}
	caps := DefaultCaps()
	caps.Pack.SubtreeFanoutMax = 1
	caps.Pack.SubtreeDoclinkMax = 32
	caps.Pack.InputBudgetTokens = 4000

	_, stats, _ := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, imp)
	if stats.DoclinkBoosts < 1 {
		t.Fatalf("doclink_boosts=%d, want ≥ 1 when doc-linked child wins fanout over equal-material sibling", stats.DoclinkBoosts)
	}
}

func TestContentionBandChildren(t *testing.T) {
	root, _ := equalMaterialSiblings()
	band := ContentionBandChildren(root)
	if len(band) != 2 {
		t.Fatalf("contention band = %d children, want 2 equal-material siblings", len(band))
	}
	unequal := &SubtreeNode{
		Path: "root", Kind: SubtreeKindDir,
		Children: []*SubtreeNode{
			{Path: "root/big", Kind: SubtreeKindDir, Material: Material{Defs: 10}},
			{Path: "root/small", Kind: SubtreeKindDir, Material: Material{Defs: 2}},
		},
	}
	if len(ContentionBandChildren(unequal)) != 0 {
		t.Fatal("unequal material siblings should not form a contention band")
	}
}

func TestAccrueDocLinksAndFanIn(t *testing.T) {
	root, _ := equalMaterialSiblings()
	imp := AccrueDocLinks(root, []string{"root/beta/b.go", "root/alpha/a.go", "outside/x.go"}, 10, nil)
	if imp["root/beta"].DocLinks != 1 || imp["root/alpha"].DocLinks != 1 {
		t.Fatalf("doc links: %+v", imp)
	}
	imp = AccrueFanIn(root, []string{"root/beta/b.go", "root/beta/b.go"}, 10, nil)
	if imp["root/beta"].FanIn != 2 {
		t.Fatalf("fan-in: %+v", imp)
	}
	imp = AccrueDocLinks(root, []string{"root/beta/b.go", "root/alpha/a.go"}, 1, nil)
	if importanceScore(imp["root/beta"])+importanceScore(imp["root/alpha"]) != 1 {
		t.Fatalf("cap=1 should accrue one link: %+v", imp)
	}
}
