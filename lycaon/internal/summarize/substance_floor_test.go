package summarize

import (
	"context"
	"testing"
)

func floorSubtree() *SubtreeNode {
	return BuildSubtree(StructuralChild{
		Path: "root", Kind: SubtreeKindDir,
		Children: []StructuralChild{
			{Path: "root/a", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "root/a/a.go", Kind: SubtreeKindFile, Defs: 3, SourceFiles: 1},
			}},
		},
	}, 6)
}

func TestApplySubstanceFloorDrillsRepresentativeFile(t *testing.T) {
	structure := []StructureCandidate{fileCand("root/a/a.go", 3, "go")}
	eng := NewEngine(fakeGather{}, DefaultCaps())
	eng.Outliner = structureOutlineProvider(structure)

	pack := ContextPack{Identity: []PackIdentity{{Path: "root/a", Kind: "dir_map"}}}
	stats := CuratorStats{}
	eng.applySubstanceFloor(context.Background(), "explain", DefaultCaps(), floorSubtree(), nil, &pack, &stats)

	if len(pack.Substance) != 1 {
		t.Fatalf("substance = %d want 1 (floor should drill one file)", len(pack.Substance))
	}
	if pack.Substance[0].Path != "root/a/a.go" {
		t.Fatalf("windowed %q want root/a/a.go", pack.Substance[0].Path)
	}
	if stats.DepthAdmits != 1 || stats.FilesOutlined != 1 {
		t.Fatalf("stats not accrued: %+v", stats)
	}
	// The drilled symbol becomes a citable anchor (selected > 0).
	if got := pickPackAnchors(pack, DefaultCaps(), 12); len(got) == 0 {
		t.Fatal("floor window should yield at least one anchor")
	}
}

func TestApplySubstanceFloorNoopWhenSubstancePresent(t *testing.T) {
	structure := []StructureCandidate{fileCand("root/a/a.go", 3, "go")}
	eng := NewEngine(fakeGather{}, DefaultCaps())
	eng.Outliner = structureOutlineProvider(structure)

	pack := ContextPack{Substance: []PackWindow{{Path: "root/z.go", Symbol: "Z", Body: "1: x"}}}
	before := len(pack.Substance)
	eng.applySubstanceFloor(context.Background(), "explain", DefaultCaps(), floorSubtree(), nil, &pack, &CuratorStats{})
	if len(pack.Substance) != before {
		t.Fatalf("floor must not touch a pack that already has substance")
	}
}

func TestApplySubstanceFloorDisabled(t *testing.T) {
	structure := []StructureCandidate{fileCand("root/a/a.go", 3, "go")}
	eng := NewEngine(fakeGather{}, DefaultCaps())
	eng.Outliner = structureOutlineProvider(structure)

	caps := DefaultCaps()
	caps.Pack.SubtreeSubstanceFloor = false
	pack := ContextPack{Identity: []PackIdentity{{Path: "root/a", Kind: "dir_map"}}}
	eng.applySubstanceFloor(context.Background(), "explain", caps, floorSubtree(), nil, &pack, &CuratorStats{})
	if len(pack.Substance) != 0 {
		t.Fatal("disabled floor must leave a map-only pack substance-free")
	}
}

func TestPickPackAnchorsUseCitedLines(t *testing.T) {
	pack := ContextPack{
		Skeleton: []PackSymbol{
			{Path: "a.go", Kind: "func", Name: "PublicFn", Line: 2},
			{Path: "a.go", Kind: "func", Name: "privateFn", Line: 6},
		},
		Substance: []PackWindow{
			{Path: "a.go", StartLine: 1, EndLine: 8, Symbol: "PublicFn",
				Body: "1: package p\n2: func PublicFn() {}\n6: func privateFn() {}\n"},
		},
	}
	anchors := pickPackAnchors(pack, DefaultCaps(), 12)
	if len(anchors) != 1 {
		t.Fatalf("want one anchor for the shared source window, got %d: %+v", len(anchors), anchors)
	}
	if anchors[0].Excerpt != "func PublicFn() {}" {
		t.Fatalf("first anchor excerpt = %q", anchors[0].Excerpt)
	}
}
