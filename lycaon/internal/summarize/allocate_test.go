package summarize

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
)

// noRerank keeps every task-ranked list lexical in tests.
var noRerank decide.Reranker

func allocateContextPackForTest(
	task string,
	root *SubtreeNode,
	structure []StructureCandidate,
	caps Caps,
	fit FitEdges,
	importance SubtreeImportance,
) (ContextPack, CuratorStats, []NextAction) {
	return allocateContextPack(context.Background(), noRerank, task, root, structure, caps, fit, importance, nil)
}

// estimatePile sizes identity and skeleton rows for structure, to set test budgets.
func estimatePile(task string, structure []StructureCandidate, caps Caps) int {
	if len(structure) == 0 {
		return 1
	}
	pile := buildDefinitionPile(structure)
	pile = rankDefinitions(context.Background(), noRerank, task, pile)
	seen := map[string]bool{}
	total := 0
	for _, d := range pile {
		if d.RelPath != "" && !seen[d.RelPath] {
			seen[d.RelPath] = true
			id := PackIdentity{Path: d.RelPath, Kind: d.FileKind, LineCount: d.LineCount, ParseHealth: parseHealthOK}
			if id.Kind == "" {
				id.Kind = StructureKindFile
			}
			total += caps.EstimateTokens(identityLine(id))
		}
		total += caps.EstimateTokens(skeletonLine(d))
	}
	if total < 1 {
		total = 1
	}
	return total
}

type mapOutlineProvider map[string]StructureCandidate

func (m mapOutlineProvider) Outline(_ context.Context, relPath string) (StructureCandidate, bool) {
	sc, ok := m[relPath]
	return sc, ok
}

func structureOutlineProvider(structure []StructureCandidate) OutlineProvider {
	m := make(mapOutlineProvider, len(structure))
	for _, sc := range structure {
		m[sc.RelPath] = sc
	}
	return m
}

func fileCand(path string, defs int, lang string) StructureCandidate {
	var syms []StructureSymbol
	var tags []string
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < defs; i++ {
		name := fmt.Sprintf("Fn%d", i)
		syms = append(syms, StructureSymbol{Kind: "func", Name: name, Line: i + 2})
		tags = append(tags, name)
		fmt.Fprintf(&b, "func %s() {}\n", name)
	}
	body := b.String()
	return StructureCandidate{
		RelPath: path, Kind: StructureKindFile, LineCount: strings.Count(body, "\n") + 1,
		Head: body, StartLine: 1, Symbols: syms, RollupRows: tags,
		ContentHash: HashString(body), Language: lang,
	}
}

func multiSubsystemFixture() (*SubtreeNode, []StructureCandidate) {
	// Three peer dirs under root — coverage must name each.
	root := BuildSubtree(StructuralChild{
		Path: "root", Kind: SubtreeKindDir,
		Children: []StructuralChild{
			{Path: "root/auth", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "root/auth/a.go", Kind: SubtreeKindFile, Defs: 8, SourceFiles: 1, Bytes: 800},
			}},
			{Path: "root/billing", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "root/billing/b.go", Kind: SubtreeKindFile, Defs: 6, SourceFiles: 1, Bytes: 600},
			}},
			{Path: "root/notify", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "root/notify/n.go", Kind: SubtreeKindFile, Defs: 4, SourceFiles: 1, Bytes: 400},
			}},
		},
	}, 6)
	structure := []StructureCandidate{
		fileCand("root/auth/a.go", 8, "go"),
		fileCand("root/billing/b.go", 6, "go"),
		fileCand("root/notify/n.go", 4, "go"),
	}
	return root, structure
}

func packMentionsPath(pack ContextPack, path string) bool {
	for _, id := range pack.Identity {
		if id.Path == path || strings.HasPrefix(id.Path, path+"/") {
			return true
		}
	}
	for _, s := range pack.Skeleton {
		if s.Path == path || strings.HasPrefix(s.Path, path+"/") {
			return true
		}
	}
	return false
}

func TestAllocateCoverageEveryChild(t *testing.T) {
	root, structure := multiSubsystemFixture()
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 400 // tight → mostly rollups, still coverage
	pack, stats, next := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, nil)

	for _, child := range []string{"root/auth", "root/billing", "root/notify"} {
		if !packMentionsPath(pack, child) {
			t.Fatalf("child %s absent from pack (identity=%d skeleton=%d)", child, len(pack.Identity), len(pack.Skeleton))
		}
	}
	if stats.ChildrenAdmitted < 3 {
		t.Fatalf("children_admitted = %d, want ≥ 3", stats.ChildrenAdmitted)
	}
	if stats.ChildrenDrilled+stats.ChildrenRolledUp < 3 {
		t.Fatalf("drilled+rolled = %d+%d, want ≥ 3", stats.ChildrenDrilled, stats.ChildrenRolledUp)
	}
	// Rolled-up children should carry a concrete summarize next_action.
	if stats.ChildrenRolledUp > 0 && len(next) == 0 {
		t.Fatal("expected drill next_actions for rolled-up children")
	}
}

// TestAllocateSubtreeMetrics pins accounting under a budget that fits.
func TestAllocateSubtreeMetrics(t *testing.T) {
	root, structure := multiSubsystemFixture()
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 100000
	_, stats, _ := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, nil)

	if stats.ChildrenAdmitted == 0 {
		t.Fatal("children_admitted = 0, want > 0")
	}
	if stats.ChildrenDrilled+stats.ChildrenRolledUp == 0 {
		t.Fatalf("drilled+rolled = %d+%d, want > 0", stats.ChildrenDrilled, stats.ChildrenRolledUp)
	}
	if stats.PrimaryLimitReason != "none" {
		t.Fatalf("primary_limit_reason = %q, want \"none\" under a budget that fits", stats.PrimaryLimitReason)
	}
}

func TestAllocateRelocatedSubtreePreservesCoverage(t *testing.T) {
	root, structure := multiSubsystemFixture()
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 500

	relocated := relocateSubtreeForTest(root, "root", "vendor/nested/root")
	var relocatedStruct []StructureCandidate
	for _, s := range structure {
		c := s
		c.RelPath = strings.Replace(c.RelPath, "root/", "vendor/nested/root/", 1)
		relocatedStruct = append(relocatedStruct, c)
	}
	packB, _, _ := allocateContextPackForTest("explain", relocated, relocatedStruct, caps, FitEdges{}, nil)
	for _, child := range []string{"vendor/nested/root/auth", "vendor/nested/root/billing", "vendor/nested/root/notify"} {
		if !packMentionsPath(packB, child) {
			t.Fatalf("relocated child %s absent", child)
		}
	}
}

func TestAllocateDampedSplitNoStarve(t *testing.T) {
	root := BuildSubtree(StructuralChild{
		Path: "root", Kind: SubtreeKindDir,
		Children: []StructuralChild{
			{Path: "root/giant", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "root/giant/g.go", Kind: SubtreeKindFile, Defs: 80, SourceFiles: 1, Bytes: 8000},
			}},
			{Path: "root/small_a", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "root/small_a/a.go", Kind: SubtreeKindFile, Defs: 2, SourceFiles: 1, Bytes: 200},
			}},
			{Path: "root/small_b", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "root/small_b/b.go", Kind: SubtreeKindFile, Defs: 2, SourceFiles: 1, Bytes: 200},
			}},
			{Path: "root/small_c", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "root/small_c/c.go", Kind: SubtreeKindFile, Defs: 2, SourceFiles: 1, Bytes: 200},
			}},
		},
	}, 6)
	structure := []StructureCandidate{
		fileCand("root/giant/g.go", 80, "go"),
		fileCand("root/small_a/a.go", 2, "go"),
		fileCand("root/small_b/b.go", 2, "go"),
		fileCand("root/small_c/c.go", 2, "go"),
	}
	caps := DefaultCaps()
	caps.Pack.SubtreeDamping = "sqrt"
	caps.Pack.InputBudgetTokens = 800
	pack, stats, _ := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, nil)

	for _, child := range []string{"root/giant", "root/small_a", "root/small_b", "root/small_c"} {
		if !packMentionsPath(pack, child) {
			t.Fatalf("sibling %s starved out of pack", child)
		}
	}
	if stats.ChildrenDrilled+stats.ChildrenRolledUp < 4 {
		t.Fatalf("expected all 4 children represented, drilled=%d rolled=%d", stats.ChildrenDrilled, stats.ChildrenRolledUp)
	}
}

func TestAllocateFitOrRollupBoundary(t *testing.T) {
	child := BuildSubtree(StructuralChild{
		Path: "pkg", Kind: SubtreeKindDir,
		Children: []StructuralChild{
			{Path: "pkg/a.go", Kind: SubtreeKindFile, Defs: 10, SourceFiles: 1, Bytes: 1000},
		},
	}, 6)
	structure := []StructureCandidate{fileCand("pkg/a.go", 10, "go")}
	caps := DefaultCaps()
	est := estimatePile("explain", structure, caps)

	// Parent with one child — allocation ≈ budget after identity.
	root := &SubtreeNode{
		Path: "root", Kind: SubtreeKindDir,
		Children: []*SubtreeNode{child},
		Material: child.Material,
	}

	// Just under: force rollup by tiny budget.
	caps.Pack.InputBudgetTokens = max(est/4, 20)
	caps.Pack.SubtreeMinRollupSharePct = 20
	packLo, statsLo, nextLo := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, nil)
	if statsLo.ChildrenRolledUp < 1 {
		t.Fatalf("expected rollup when est=%d budget=%d; stats=%+v skeleton=%v",
			est, caps.Pack.InputBudgetTokens, statsLo, packLo.Skeleton)
	}
	foundDrill := false
	for _, na := range nextLo {
		if na.Tool == "summarize" && (na.Path == "pkg" || strings.HasPrefix(na.Path, "pkg/")) {
			foundDrill = true
			break
		}
	}
	if !foundDrill {
		t.Fatalf("expected summarize next_action for rolled-up pkg; got %#v", nextLo)
	}

	// Generous budget → drill.
	caps.Pack.InputBudgetTokens = est*4 + 200
	_, statsHi, _ := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, nil)
	if statsHi.ChildrenDrilled < 1 {
		t.Fatalf("expected drill under large budget; stats=%+v", statsHi)
	}
}

func TestAllocateRecursionDepthBound(t *testing.T) {
	root := BuildSubtree(StructuralChild{
		Path: "a", Kind: SubtreeKindDir,
		Children: []StructuralChild{
			{Path: "a/b", Kind: SubtreeKindDir, Children: []StructuralChild{
				{Path: "a/b/c", Kind: SubtreeKindDir, Children: []StructuralChild{
					{Path: "a/b/c/d.go", Kind: SubtreeKindFile, Defs: 3, SourceFiles: 1},
				}},
			}},
		},
	}, 1) // maxDepth=1 → deeper nodes RollupOnly
	structure := []StructureCandidate{fileCand("a/b/c/d.go", 3, "go")}
	caps := DefaultCaps()
	caps.Gather.MaxListDepth = 1
	caps.Pack.InputBudgetTokens = 2000
	pack, stats, _ := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, nil)
	if stats.RecursionDepth > 1 {
		t.Fatalf("recursion_depth = %d, want ≤ 1", stats.RecursionDepth)
	}
	hasRollup := false
	for _, s := range pack.Skeleton {
		if s.Kind == KindDirectoryRollup {
			hasRollup = true
			break
		}
	}
	if !hasRollup && stats.ChildrenRolledUp == 0 {
		t.Fatal("expected rollup at depth bound")
	}
}

func TestAllocateOneReduce(t *testing.T) {
	cases := []struct {
		name      string
		structure []StructureCandidate
		tree      *SubtreeNode
	}{
		{
			name:      "file",
			structure: []StructureCandidate{fileCand("f.go", 2, "go")},
			tree: BuildSubtree(StructuralChild{
				Path: "f.go", Kind: SubtreeKindFile, Defs: 2, SourceFiles: 1,
			}, 6),
		},
		{
			name:      "dir",
			structure: []StructureCandidate{fileCand("pkg/a.go", 3, "go")},
			tree: BuildSubtree(StructuralChild{
				Path: "pkg", Kind: SubtreeKindDir,
				Children: []StructuralChild{
					{Path: "pkg/a.go", Kind: SubtreeKindFile, Defs: 3, SourceFiles: 1},
				},
			}, 6),
		},
		{
			name: "big-dir",
			structure: []StructureCandidate{
				fileCand("root/a/a.go", 20, "go"),
				fileCand("root/b/b.go", 20, "go"),
				fileCand("root/c/c.go", 20, "go"),
			},
			tree: BuildSubtree(StructuralChild{
				Path: "root", Kind: SubtreeKindDir,
				Children: []StructuralChild{
					{Path: "root/a", Kind: SubtreeKindDir, Children: []StructuralChild{
						{Path: "root/a/a.go", Kind: SubtreeKindFile, Defs: 20, SourceFiles: 1},
					}},
					{Path: "root/b", Kind: SubtreeKindDir, Children: []StructuralChild{
						{Path: "root/b/b.go", Kind: SubtreeKindFile, Defs: 20, SourceFiles: 1},
					}},
					{Path: "root/c", Kind: SubtreeKindDir, Children: []StructuralChild{
						{Path: "root/c/c.go", Kind: SubtreeKindFile, Defs: 20, SourceFiles: 1},
					}},
				},
			}, 6),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := NewEngine(fakeGather{res: GatherResult{
				Mode: ModeRepo, Structure: tc.structure, Subtree: tc.tree,
				Stats: GatherStats{Mode: ModeRepo, Candidates: len(tc.structure), UseStructure: true},
			}}, DefaultCaps())
			eng.Outliner = structureOutlineProvider(tc.structure)
			_, err := eng.Run(context.Background(), Request{Task: "explain", Path: "x", MaxAnchors: 12})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
		})
	}
}

func TestAllocateBudgetRespectedRecursive(t *testing.T) {
	root, _ := multiSubsystemFixture()
	// Inflate defs so recursion would overspend without a hard budget.
	structure := []StructureCandidate{
		fileCand("root/auth/a.go", 40, "go"),
		fileCand("root/billing/b.go", 40, "go"),
		fileCand("root/notify/n.go", 40, "go"),
	}
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 300
	pack, stats, _ := allocateContextPackForTest("explain", root, structure, caps, FitEdges{}, nil)
	est := caps.EstimatePackTokens(pack)
	// Allow a small overshoot for coverage-forced rollup rows.
	if est > caps.Pack.InputBudgetTokens+40 {
		t.Fatalf("est_tokens=%d budget=%d spent=%d", est, caps.Pack.InputBudgetTokens, stats.BudgetTokensSpent)
	}
}

func TestAllocateSmallTargetParity(t *testing.T) {
	structure := []StructureCandidate{fileCand("pkg/a.go", 3, "go")}
	caps := DefaultCaps()
	packFlat, statsFlat, nextFlat := assembleContextPack(context.Background(), noRerank, "explain", structure, caps, FitEdges{})

	// Leaf / no-children tree → allocate delegates to the same knapsack.
	leaf := &SubtreeNode{Path: "pkg/a.go", Kind: SubtreeKindFile, Material: Material{Defs: 3, SourceFiles: 1}}
	packAlloc, statsAlloc, nextAlloc := allocateContextPackForTest("explain", leaf, structure, caps, FitEdges{}, nil)
	if !reflect.DeepEqual(packFlat, packAlloc) {
		t.Fatalf("leaf allocate pack != flat knapsack\nflat=%+v\nalloc=%+v", packFlat, packAlloc)
	}
	if statsFlat.BudgetTokensSpent != statsAlloc.BudgetTokensSpent ||
		statsFlat.BreadthAdmits != statsAlloc.BreadthAdmits ||
		statsFlat.DepthAdmits != statsAlloc.DepthAdmits {
		t.Fatalf("stats drifted: flat=%+v alloc=%+v", statsFlat, statsAlloc)
	}
	if len(nextFlat) != len(nextAlloc) {
		t.Fatalf("next_actions len %d vs %d", len(nextFlat), len(nextAlloc))
	}

	// The flat path retains the single-file entry.
	packNil, _, _ := allocateContextPackForTest("explain", nil, structure, caps, FitEdges{}, nil)
	if !reflect.DeepEqual(packFlat, packNil) {
		t.Fatal("nil-root allocate diverged from flat knapsack")
	}
}

func TestBigTargetCompleteWhenCovered(t *testing.T) {
	root, _ := multiSubsystemFixture()
	// Many defs → rollups dominate.
	structure := []StructureCandidate{
		fileCand("root/auth/a.go", 50, "go"),
		fileCand("root/billing/b.go", 50, "go"),
		fileCand("root/notify/n.go", 50, "go"),
	}
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 200
	eng := NewEngine(fakeGather{res: GatherResult{
		Mode: ModeRepo, Structure: structure, Subtree: root,
		Stats: GatherStats{Mode: ModeRepo, Candidates: len(structure), UseStructure: true},
	}}, caps)
	res, err := eng.Run(context.Background(), Request{Task: "explain systems", Path: "root", MaxAnchors: 12})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Coverage.Complete {
		t.Fatalf("coverage = %+v, want complete", res.Coverage)
	}
	cur := res.Orchestration.Curator
	if cur.ChildrenRolledUp == 0 && cur.ChildrenDrilled == 0 {
		t.Fatalf("expected subtree curator metrics; got %+v", cur)
	}
	if cur.PrimaryLimitReason != "pack_budget" && cur.PrimaryLimitReason != "none" {
		t.Fatalf("primary_limit_reason=%q", cur.PrimaryLimitReason)
	}
}

func TestAllocateFlatLeafDirDrills(t *testing.T) {
	// Entry-cost fit preserves substance for flat packages.
	const n = 40
	var children []StructuralChild
	var structure []StructureCandidate
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("pkg/f%02d.go", i)
		if i == 5 {
			path = "pkg/abort_impl.go"
		}
		children = append(children, StructuralChild{
			Path: path, Kind: SubtreeKindFile, Defs: 30, SourceFiles: 1, Bytes: 3000,
		})
		structure = append(structure, fileCand(path, 30, "go"))
	}
	root := BuildSubtree(StructuralChild{
		Path: "pkg", Kind: SubtreeKindDir, Children: children,
	}, 6)
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 4500
	provider := structureOutlineProvider(structure)

	pack, stats, _ := allocateContextPack(context.Background(), noRerank, "how does abort work?", root, structure, caps, FitEdges{}, nil, provider)
	if stats.ChildrenDrilled < 1 {
		t.Fatalf("flat leaf dir drilled=%d (want ≥1); rolled=%d forced=%d",
			stats.ChildrenDrilled, stats.ChildrenRolledUp, stats.ForcedDrills)
	}
	maxDrills := caps.Pack.SubtreeMaxDrills
	if maxDrills > 0 && stats.ChildrenDrilled > maxDrills {
		t.Fatalf("drilled=%d exceeds subtree_max_drills=%d", stats.ChildrenDrilled, maxDrills)
	}
	if len(pack.Substance) == 0 && len(pack.Skeleton) == 0 {
		t.Fatal("expected non-empty pack after flat-dir drills")
	}
	// Task path IDF should prefer abort_impl among the fanout-kept set.
	drilledAbort := false
	for _, id := range pack.Identity {
		if strings.Contains(id.Path, "abort_impl") {
			drilledAbort = true
			break
		}
	}
	for _, s := range pack.Skeleton {
		if strings.Contains(s.Path, "abort_impl") && s.Kind != KindDirectoryRollup {
			drilledAbort = true
			break
		}
	}
	for _, w := range pack.Substance {
		if strings.Contains(w.Path, "abort_impl") {
			drilledAbort = true
			break
		}
	}
	if !drilledAbort {
		t.Fatalf("task “abort” should depth-admit abort_impl.go; identity=%v skeleton paths sample", pack.Identity)
	}
}

func TestAllocateMaxDrillCap(t *testing.T) {
	const n = 30
	var children []StructuralChild
	var structure []StructureCandidate
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("pkg/f%02d.go", i)
		children = append(children, StructuralChild{
			Path: path, Kind: SubtreeKindFile, Defs: 20, SourceFiles: 1, Bytes: 2000,
		})
		structure = append(structure, fileCand(path, 20, "go"))
	}
	root := BuildSubtree(StructuralChild{
		Path: "pkg", Kind: SubtreeKindDir, Children: children,
	}, 6)
	caps := DefaultCaps()
	caps.Pack.SubtreeMaxDrills = 3
	caps.Pack.SubtreeMinDrills = 2
	_, stats, next := allocateContextPack(context.Background(), noRerank, "overview", root, structure, caps, FitEdges{}, nil, structureOutlineProvider(structure))
	if stats.ChildrenDrilled != 3 {
		t.Fatalf("drilled=%d, want exactly subtree_max_drills=3 (rolled=%d)", stats.ChildrenDrilled, stats.ChildrenRolledUp)
	}
	if stats.ChildrenRolledUp < 1 {
		t.Fatal("expected remaining fanout children rolled up")
	}
	summarizeZooms := 0
	for _, a := range next {
		if a.Tool == "summarize" {
			summarizeZooms++
		}
	}
	if summarizeZooms < 1 {
		t.Fatalf("expected rollup summarize next_actions; got %#v", next)
	}
}

func TestAllocateMaxFilesReadCapsOutlines(t *testing.T) {
	const n = 10
	children := make([]StructuralChild, 0, n)
	structure := make([]StructureCandidate, 0, n)
	for i := range n {
		path := fmt.Sprintf("pkg/f%02d.go", i)
		children = append(children, StructuralChild{
			Path: path, Kind: SubtreeKindFile, Defs: 4, SourceFiles: 1, Bytes: 400,
		})
		structure = append(structure, fileCand(path, 4, "go"))
	}
	root := BuildSubtree(StructuralChild{
		Path: "pkg", Kind: SubtreeKindDir, Children: children,
	}, 6)
	caps := DefaultCaps()
	caps.Gather.MaxFilesRead = 3
	caps.Pack.InputBudgetTokens = 100_000
	caps.Pack.SubtreeMaxDrills = 0

	_, stats, _ := allocateContextPack(
		context.Background(), noRerank, "", root, nil, caps, FitEdges{}, nil,
		structureOutlineProvider(structure),
	)
	if stats.FilesOutlined != 3 {
		t.Fatalf("files_outlined=%d, want max_files_read=3", stats.FilesOutlined)
	}
}

func TestFitEstimatePileFileUsesEntryCost(t *testing.T) {
	node := &SubtreeNode{
		Path: "pkg/big.go", Kind: SubtreeKindFile,
		Material: Material{Defs: 40, SourceFiles: 1, Bytes: 8000},
	}
	caps := DefaultCaps()
	full := estimatePileFromMaterial(node, caps)
	entry := fitEstimatePile(node, caps)
	if entry >= full {
		t.Fatalf("entry cost %d should be ≪ full pile %d for a multi-def file", entry, full)
	}
	if entry < 1 {
		t.Fatal("entry cost must be positive")
	}
}

func TestRollupRowPrimitive(t *testing.T) {
	child := &SubtreeNode{
		Path: "pkg/auth", Kind: SubtreeKindDir,
		Material: Material{Defs: 5, SourceFiles: 2},
	}
	scoped := []StructureCandidate{
		fileCand("pkg/auth/a.go", 3, "go"),
		fileCand("pkg/auth/b.go", 2, "go"),
	}
	row := rollupRow(child, scoped)
	if row.Kind != KindDirectoryRollup {
		t.Fatalf("kind=%q", row.Kind)
	}
	if row.Path != "pkg/auth" {
		t.Fatalf("path=%q", row.Path)
	}
	if !strings.Contains(row.Name, "2 files") {
		t.Fatalf("detail missing file count: %q", row.Name)
	}
	if !strings.Contains(row.Name, "go") {
		t.Fatalf("detail missing lang: %q", row.Name)
	}
	if !strings.Contains(row.Name, "top:") {
		t.Fatalf("detail missing top symbols: %q", row.Name)
	}
	caps := DefaultCaps()
	cost := caps.EstimateTokens(rollupSkeletonLine(row))
	if cost < 1 || cost > 80 {
		t.Fatalf("rollup cost %d looks wrong (want cheap skeleton line)", cost)
	}
}
