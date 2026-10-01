package summarize

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestMaterialPrefersDefsThenFiles(t *testing.T) {
	parsed := &SubtreeNode{Path: "a", Material: Material{Defs: 10, SourceFiles: 2, Bytes: 100}}
	unparsed := &SubtreeNode{Path: "b", Material: Material{Defs: 0, SourceFiles: 50, Bytes: 999}}
	if material(parsed) != 10 {
		t.Fatalf("parsed score = %d, want Defs 10", material(parsed))
	}
	if material(unparsed) != 50 {
		t.Fatalf("unparsed score = %d, want SourceFiles 50", material(unparsed))
	}
	// Bytes only tiebreaks when primary scores equal.
	root := &SubtreeNode{
		Path: "root", Kind: SubtreeKindDir,
		Children: []*SubtreeNode{
			{Path: "small", Material: Material{SourceFiles: 5, Bytes: 10}},
			{Path: "big", Material: Material{SourceFiles: 5, Bytes: 1000}},
		},
	}
	ranked := rankChildren(root)
	if ranked[0].Path != "big" {
		t.Fatalf("tiebreak order = %s then %s, want big first", ranked[0].Path, ranked[1].Path)
	}
}

func TestSubtreeScaleInvariantRelocation(t *testing.T) {
	fixture := BuildSubtree(StructuralChild{
		Path: "pkg", Kind: SubtreeKindDir,
		Children: []StructuralChild{
			{Path: "pkg/a.go", Kind: SubtreeKindFile, Defs: 3, SourceFiles: 1, Bytes: 100},
			{Path: "pkg/b.go", Kind: SubtreeKindFile, Defs: 1, SourceFiles: 1, Bytes: 50},
			{Path: "pkg/sub", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "pkg/sub/c.go", Kind: SubtreeKindFile, Defs: 2, SourceFiles: 1, Bytes: 80},
			}},
		},
	}, 6)

	relocated := relocateSubtreeForTest(fixture, "pkg", "vendor/nested/pkg")
	if material(fixture) != material(relocated) {
		t.Fatalf("material changed on relocate: %d vs %d", material(fixture), material(relocated))
	}
	if fixture.Material != relocated.Material {
		t.Fatalf("Material struct changed: %+v vs %+v", fixture.Material, relocated.Material)
	}
	origOrder := childPaths(fixture)
	// Compare relative child ordering by material, ignoring absolute paths.
	origScores := childScores(fixture)
	relocScores := childScores(relocated)
	if !reflect.DeepEqual(origScores, relocScores) {
		t.Fatalf("child material order changed: %v vs %v (paths %v)", origScores, relocScores, origOrder)
	}
}

func relocateSubtreeForTest(n *SubtreeNode, oldPrefix, newPrefix string) *SubtreeNode {
	out := CloneSubtree(n)
	var relocate func(*SubtreeNode)
	relocate = func(node *SubtreeNode) {
		if node == nil {
			return
		}
		if node.Path == oldPrefix {
			node.Path = newPrefix
		} else if strings.HasPrefix(node.Path, oldPrefix+"/") {
			node.Path = newPrefix + node.Path[len(oldPrefix):]
		}
		for _, child := range node.Children {
			relocate(child)
		}
	}
	relocate(out)
	return out
}

func childPaths(n *SubtreeNode) []string {
	var out []string
	for _, c := range n.Children {
		out = append(out, c.Path)
	}
	return out
}

func childScores(n *SubtreeNode) []int {
	var out []int
	for _, c := range rankChildren(n) {
		out = append(out, material(c))
	}
	return out
}

func TestSubtreeShapesBalanced(t *testing.T) {
	var children []StructuralChild
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		children = append(children, StructuralChild{
			Path: name, Kind: SubtreeKindDir,
			Children: []StructuralChild{
				{Path: name + "/f.go", Kind: SubtreeKindFile, Defs: 4, SourceFiles: 1, Bytes: 200},
			},
		})
	}
	root := BuildSubtree(StructuralChild{Path: "root", Kind: SubtreeKindDir, Children: children}, 6)
	if len(root.Children) != 3 {
		t.Fatalf("children = %d", len(root.Children))
	}
	scores := childScores(root)
	for i := 1; i < len(scores); i++ {
		if scores[i] != scores[0] {
			t.Fatalf("balanced scores = %v, want equal", scores)
		}
	}
	// Stable path order when material ties.
	ranked := rankChildren(root)
	if ranked[0].Path != "alpha" || ranked[2].Path != "charlie" {
		t.Fatalf("stable order = %v", childPaths(&SubtreeNode{Children: ranked}))
	}
}

func TestSubtreeShapeGiantChild(t *testing.T) {
	root := BuildSubtree(StructuralChild{
		Path: "root", Kind: SubtreeKindDir,
		Children: []StructuralChild{
			{Path: "tiny", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "tiny/a.go", Kind: SubtreeKindFile, Defs: 1, SourceFiles: 1},
			}},
			{Path: "giant", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "giant/a.go", Kind: SubtreeKindFile, Defs: 40, SourceFiles: 1},
				{Path: "giant/b.go", Kind: SubtreeKindFile, Defs: 40, SourceFiles: 1},
			}},
			{Path: "mid", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "mid/a.go", Kind: SubtreeKindFile, Defs: 5, SourceFiles: 1},
			}},
		},
	}, 6)
	ranked := rankChildren(root)
	if ranked[0].Path != "giant" {
		t.Fatalf("first = %s, want giant", ranked[0].Path)
	}
	if len(ranked) != 3 {
		t.Fatalf("siblings dropped: %v", childPaths(&SubtreeNode{Children: ranked}))
	}
}

func TestSubtreeDeepNarrowDepthBound(t *testing.T) {
	// depth: root(0) -> d1(1) -> d2(2) -> d3(3) -> file
	deep := StructuralChild{Path: "d1", Kind: SubtreeKindDir, Children: []StructuralChild{
		{Path: "d1/d2", Kind: SubtreeKindDir, Children: []StructuralChild{
			{Path: "d1/d2/d3", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "d1/d2/d3/f.go", Kind: SubtreeKindFile, Defs: 2, SourceFiles: 1},
			}},
		}},
	}}
	root := BuildSubtree(StructuralChild{Path: "root", Kind: SubtreeKindDir, Children: []StructuralChild{deep}}, 2)
	d1 := root.Children[0]
	if d1.RollupOnly {
		t.Fatal("d1 at depth 1 should still expand")
	}
	d2 := d1.Children[0]
	if !d2.RollupOnly {
		t.Fatal("d2 at depth 2 should be RollupOnly when maxDepth=2")
	}
	if len(d2.Children) != 0 {
		t.Fatalf("rollup-only node must clear children, got %d", len(d2.Children))
	}
}

func TestAllocateSmallTargetParityEngine(t *testing.T) {
	// A single-file gather with a leaf Subtree delegates to the flat knapsack.
	structure := []StructureCandidate{{
		RelPath: "pkg/a.go", Kind: StructureKindFile, LineCount: 20,
		Head: "package pkg\nfunc Main() {}\n", StartLine: 1,
		Symbols:     []StructureSymbol{{Kind: "func", Name: "Main", Line: 2}},
		ContentHash: HashString("pkg/a.go"),
	}}
	caps := DefaultCaps()
	packFlat, _, _ := assembleContextPack(context.Background(), noRerank, "explain", structure, caps, FitEdges{})

	tree := BuildSubtree(StructuralChild{
		Path: "pkg/a.go", Kind: SubtreeKindFile, Defs: 1, SourceFiles: 1,
	}, 6)
	packAlloc, _, _ := allocateContextPackForTest("explain", tree, structure, caps, FitEdges{}, nil)
	if !reflect.DeepEqual(packFlat, packAlloc) {
		t.Fatal("single-file allocate pack diverged from 159 knapsack")
	}

	eng := NewEngine(fakeGather{res: GatherResult{
		Mode: ModeRepo, Structure: structure, Subtree: tree,
		Stats: GatherStats{Mode: ModeRepo, Candidates: 1, UseStructure: true},
	}}, caps)
	res, err := eng.Run(context.Background(), Request{Task: "explain", Path: "pkg/a.go", MaxAnchors: 12})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Pack.Skeleton) == 0 {
		t.Fatal("expected assembled pack skeleton from allocate parity path")
	}
}
