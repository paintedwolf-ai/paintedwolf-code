package summarize

import (
	"fmt"
	"testing"
)

func TestAllocateDrillCapReusesRolledUpDepthBudget(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 2200
	caps.Pack.SubtreeMaxDrills = 3
	var children []StructuralChild
	var structure []StructureCandidate
	for i := range 24 {
		path := fmt.Sprintf("pkg/file%02d.go", i)
		children = append(children, StructuralChild{Path: path, Kind: SubtreeKindFile, Defs: 20, SourceFiles: 1})
		structure = append(structure, fileCand(path, 20, "go"))
	}
	root := BuildSubtree(StructuralChild{Path: "pkg", Kind: SubtreeKindDir, Children: children}, 6)
	pack, stats, _ := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, nil)
	if stats.ChildrenDrilled != 3 || stats.ChildrenRolledUp != 21 {
		t.Fatalf("drill cap changed: drilled=%d rolled=%d", stats.ChildrenDrilled, stats.ChildrenRolledUp)
	}
	if len(pack.Substance) < 3 {
		t.Fatalf("selected children lost depth despite available budget: windows=%d spent=%d budget=%d", len(pack.Substance), stats.BudgetTokensSpent, caps.Pack.InputBudgetTokens)
	}
	if got := caps.EstimatePackTokens(pack); got > caps.Pack.InputBudgetTokens {
		t.Fatalf("reallocation exceeded budget: got=%d cap=%d", got, caps.Pack.InputBudgetTokens)
	}
	for _, child := range children {
		if !packMentionsPath(pack, child.Path) {
			t.Fatalf("reallocation lost coverage for %s", child.Path)
		}
	}
}

func TestRedistributeRolledUpDepthBudgetConservesAllocation(t *testing.T) {
	caps := DefaultCaps()
	plans := []childPlan{
		{child: &SubtreeNode{Path: "a.go", Kind: SubtreeKindFile}, bi: 100, drill: true},
		{child: &SubtreeNode{Path: "b.go", Kind: SubtreeKindFile}, bi: 300},
		{child: &SubtreeNode{Path: "c.go", Kind: SubtreeKindFile}, bi: 200, drill: true},
		{child: &SubtreeNode{Path: "d.go", Kind: SubtreeKindFile}, bi: 400},
	}
	redistributeRolledUpDepthBudget(plans, caps, 1000)
	total := 0
	for _, plan := range plans {
		if plan.drill {
			total += plan.bi
		} else {
			total += caps.EstimateTokens(rollupSkeletonLine(rollupRow(plan.child, nil)))
		}
	}
	if total != 1000 || plans[0].bi <= 100 || plans[2].bi <= 200 {
		t.Fatalf("allocation not conserved: total=%d shares=%d,%d", total, plans[0].bi, plans[2].bi)
	}
}

func TestRedistributeRolledUpDepthBudgetWithoutRecipient(t *testing.T) {
	caps := DefaultCaps()
	plans := []childPlan{{child: &SubtreeNode{Path: "a.go", Kind: SubtreeKindFile}, bi: 100}}
	redistributeRolledUpDepthBudget(plans, caps, 100)
	if plans[0].bi != 100 || plans[0].drill {
		t.Fatal("reallocation must not change drill selection")
	}
}

func TestRedistributeRolledUpDepthBudgetDoesNotDoubleSpendRescue(t *testing.T) {
	caps := DefaultCaps()
	plans := []childPlan{
		{child: &SubtreeNode{Path: "a.go", Kind: SubtreeKindFile}, bi: 400, drill: true, forced: true},
		{child: &SubtreeNode{Path: "b.go", Kind: SubtreeKindFile}, bi: 400, drill: true, forced: true},
		{child: &SubtreeNode{Path: "c.go", Kind: SubtreeKindFile}, bi: 400},
	}
	redistributeRolledUpDepthBudget(plans, caps, 1000)
	coverage := caps.EstimateTokens(rollupSkeletonLine(rollupRow(plans[2].child, nil)))
	if got := plans[0].bi + plans[1].bi + coverage; got != 1000 {
		t.Fatalf("rescue allocation counted twice: %d", got)
	}
}
