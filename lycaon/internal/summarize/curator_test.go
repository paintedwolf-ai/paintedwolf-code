package summarize

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestRankDefinitionsSymbolGranular(t *testing.T) {
	defs := []DefinitionItem{
		{RelPath: "pkg/a.go", Kind: "paragraph", Name: "copyright notice", Line: 1},
		{RelPath: "pkg/a.go", Kind: "func", Name: "helper", Line: 40},
		{RelPath: "pkg/a.go", Kind: "type", Name: "Session", Line: 10},
		{RelPath: "pkg/a.go", Kind: "func", Name: "Abort", Line: 20},
	}
	ranked := rankDefinitions(context.Background(), noRerank, "Abort Session", defs)
	if ranked[0].Name != "Abort" && ranked[0].Name != "Session" {
		t.Fatalf("top = %+v, want Abort or Session", ranked[0])
	}
	// Definitions outrank paragraphs without task overlap.
	ranked2 := rankDefinitions(context.Background(), noRerank, "zzzz", defs)
	if ranked2[len(ranked2)-1].Kind != "paragraph" {
		t.Fatalf("paragraph should sort last under structural salience; got %+v", ranked2)
	}
}

func TestFillBreadthThenDepth(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 200
	caps.Pack.SizeDivisor = 1
	caps.Pack.BreadthFractionPct = 40
	caps.Gather.SymbolWindowLines = 6
	structure := []StructureCandidate{{
		RelPath: "pkg/a.go", Kind: StructureKindFile, LineCount: 80, StartLine: 1,
		Head: strings.Repeat("func line\n", 80),
		Symbols: []StructureSymbol{
			{Kind: "func", Name: "A", Line: 10},
			{Kind: "func", Name: "B", Line: 30},
			{Kind: "func", Name: "C", Line: 50},
		},
		ContentHash: "h",
	}}
	pack, stats, _ := assembleContextPack(context.Background(), noRerank, "explain", structure, caps, FitEdges{})
	if stats.BreadthAdmits == 0 {
		t.Fatal("expected breadth admits")
	}
	if len(pack.Skeleton) == 0 {
		t.Fatal("expected skeleton")
	}
	if len(pack.Substance) == 0 {
		t.Fatal("expected substance windows after breadth")
	}
	if caps.EstimatePackTokens(pack) > caps.Pack.InputBudgetTokens {
		t.Fatalf("pack over budget")
	}
}

func TestSubstanceIsSymbolWindow(t *testing.T) {
	caps := DefaultCaps()
	caps.Gather.SymbolWindowLines = 5
	head := ""
	for i := 1; i <= 40; i++ {
		head += "line body\n"
	}
	structure := []StructureCandidate{{
		RelPath: "pkg/a.go", Kind: StructureKindFile, LineCount: 40, StartLine: 1,
		Head:        head,
		Symbols:     []StructureSymbol{{Kind: "func", Name: "Target", Line: 20}},
		ContentHash: "h",
	}}
	pack, _, _ := assembleContextPack(context.Background(), noRerank, "Target", structure, caps, FitEdges{})
	if len(pack.Substance) == 0 {
		t.Fatal("expected window")
	}
	w := pack.Substance[0]
	if w.StartLine > 20 || w.EndLine < 20 {
		t.Fatalf("window %d-%d does not cover symbol line 20", w.StartLine, w.EndLine)
	}
	if w.StartLine == 1 && w.EndLine >= 40 {
		t.Fatal("substance must be a window, not the full first-N head")
	}
	if !strings.Contains(w.Body, "20:") {
		t.Fatalf("window body missing numbered symbol line: %q", w.Body)
	}
}

func TestDirFillDeepensMultipleFiles(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 4000
	caps.Gather.SymbolWindowLines = 8
	structure := []StructureCandidate{
		{RelPath: "pkg", Kind: StructureKindDirMap, RollupRows: []string{"a.go", "b.go", "c.go"}},
	}
	for i, name := range []string{"a.go", "b.go", "c.go"} {
		head := ""
		for n := 1; n <= 30; n++ {
			head += "code line\n"
		}
		structure = append(structure, StructureCandidate{
			RelPath: "pkg/" + name, Kind: StructureKindFile, LineCount: 30, StartLine: 1,
			Head: head,
			Symbols: []StructureSymbol{
				{Kind: "func", Name: "Fn" + name[:1], Line: 5 + i},
				{Kind: "type", Name: "T" + name[:1], Line: 15},
			},
			ContentHash: name,
		})
	}
	pack, stats, _ := assembleContextPack(context.Background(), noRerank, "explain pkg", structure, caps, FitEdges{})
	hasMap := false
	for _, s := range pack.Skeleton {
		if s.Kind == "directory_map" {
			hasMap = true
		}
	}
	if !hasMap {
		t.Fatal("expected directory_map in skeleton")
	}
	paths := map[string]bool{}
	for _, w := range pack.Substance {
		paths[w.Path] = true
	}
	if len(paths) < 2 {
		t.Fatalf("expected windows across multiple files, got %v (deepen=%d)", paths, stats.DeepenHits)
	}
	if stats.DeepenHits < 2 {
		t.Fatalf("deepen_hits=%d, want ≥2", stats.DeepenHits)
	}
}

func TestBudgetRespected(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 500
	caps.Pack.SizeDivisor = 4
	structure := structureCandidates(30, 40)
	for i := range structure {
		var b strings.Builder
		for n := 0; n < 40; n++ {
			b.WriteString("xxxxxxxxxxxxxxxx\n")
		}
		structure[i].Head = b.String()
		structure[i].StartLine = 1
	}
	pack, _, _ := assembleContextPack(context.Background(), noRerank, "wide", structure, caps, FitEdges{})
	if got := caps.EstimatePackTokens(pack); got > caps.Pack.InputBudgetTokens {
		t.Fatalf("est %d > budget %d", got, caps.Pack.InputBudgetTokens)
	}
}

func TestFillTaskTokenBoundary(t *testing.T) {
	structure := structureCandidates(3, 20)
	pile1 := buildDefinitionPile(structure)
	pile2 := buildDefinitionPile(structure)
	if len(pile1) != len(pile2) {
		t.Fatal("pile size must not depend on task")
	}
	// Ranking reorders but does not add paths.
	r1 := rankDefinitions(context.Background(), noRerank, "f0", pile1)
	r2 := rankDefinitions(context.Background(), noRerank, "zzzz-no-overlap", pile2)
	paths := func(defs []DefinitionItem) map[string]bool {
		m := map[string]bool{}
		for _, d := range defs {
			m[d.RelPath] = true
		}
		return m
	}
	p1, p2 := paths(r1), paths(r2)
	if len(p1) != len(p2) {
		t.Fatalf("task must not change path set: %v vs %v", p1, p2)
	}
}

func TestCuratorMetrics(t *testing.T) {
	caps := DefaultCaps()
	structure := structureCandidates(5, 30)
	for i := range structure {
		structure[i].Head = "package pkg\nfunc Main() {}\n"
		structure[i].StartLine = 1
	}
	_, stats, _ := assembleContextPack(context.Background(), noRerank, "explain", structure, caps, FitEdges{})
	if stats.BudgetTokensSpent <= 0 {
		t.Fatal("expected budget_tokens_spent")
	}
	if stats.BreadthAdmits <= 0 {
		t.Fatal("expected breadth_admits")
	}
	if stats.PrimaryLimitReason == "" {
		t.Fatal("expected primary_limit_reason")
	}
}

func TestLeftoverNextActionsTopK(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 60
	caps.Pack.SizeDivisor = 1
	caps.Pack.BreadthFractionPct = 50
	structure := structureCandidates(20, 50)
	for i := range structure {
		structure[i].Head = strings.Repeat("body line with content\n", 50)
		structure[i].StartLine = 1
	}
	pack, _, next := assembleContextPack(context.Background(), noRerank, "wide", structure, caps, FitEdges{})
	if len(pack.Gaps) == 0 {
		t.Fatal("expected gaps when over budget")
	}
	if len(next) == 0 {
		t.Fatal("expected concrete next_actions")
	}
	for _, na := range next {
		if na.Tool != "read" || na.Path == "" {
			t.Fatalf("next_action = %+v, want concrete read path", na)
		}
		if strings.Contains(strings.ToLower(na.Why), "narrow") {
			t.Fatalf("generic narrow why: %q", na.Why)
		}
	}
}

func TestPackOneReduce(t *testing.T) {
	structure := structureCandidates(8, 40)
	for i := range structure {
		structure[i].Head = "package pkg\nfunc Main() {}\n"
		structure[i].StartLine = 1
	}
	eng := NewEngine(fakeGather{GatherResult{
		Mode: ModeRepo, Structure: structure,
		Stats: GatherStats{Mode: ModeRepo, Candidates: 8, PathIsDir: true, UseStructure: true},
	}}, DefaultCaps())
	res, err := eng.Run(context.Background(), Request{Task: "explain", MaxAnchors: 12})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Orchestration.Curator.BreadthAdmits == 0 {
		t.Fatal("expected curator metrics on result")
	}
	if len(res.Pack.Skeleton) == 0 {
		t.Fatal("expected assembled ContextPack skeleton")
	}
}

func TestFillNoDefinitionsDegrades(t *testing.T) {
	md := StructureCandidate{
		RelPath: "docs/note.md", Kind: StructureKindFile, LineCount: 10, StartLine: 1,
		Head: "# Title\n\nIntro paragraph.\n\n## Section\n\nMore text.\n",
	}
	c, health := ensureDefinitionsForCandidate(md)
	if health != parseHealthDegradedHeaders {
		t.Fatalf("health = %q", health)
	}
	if len(c.Symbols) < 2 {
		t.Fatalf("symbols = %+v, want headers", c.Symbols)
	}
	plain := StructureCandidate{
		RelPath: "notes.txt", Kind: StructureKindFile, LineCount: 5, StartLine: 1,
		Head: "First paragraph here.\n\nSecond paragraph here.\n",
	}
	c2, health2 := ensureDefinitionsForCandidate(plain)
	if len(c2.Symbols) == 0 {
		t.Fatal("plain text must degrade to paragraphs, never empty")
	}
	if health2 != parseHealthDegradedParagraphs {
		t.Fatalf("health2 = %q", health2)
	}
	caps := DefaultCaps()
	pack, _, _ := assembleContextPack(context.Background(), noRerank, "note", []StructureCandidate{md}, caps, FitEdges{})
	if len(pack.Skeleton) == 0 {
		t.Fatal("degraded md must produce skeleton")
	}
	if pack.Identity[0].ParseHealth != parseHealthDegradedHeaders {
		t.Fatalf("identity parse health = %q", pack.Identity[0].ParseHealth)
	}
}

func TestMultiPathIdentityLeavesBudgetForSubstance(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 800
	caps.Pack.SizeDivisor = 4
	caps.Gather.SymbolWindowLines = 6
	var structure []StructureCandidate
	structure = append(structure, StructureCandidate{
		RelPath: "pkg", Kind: StructureKindDirMap, RollupRows: []string{"f0.go"},
	})
	for i := 0; i < 40; i++ {
		name := "f" + strconv.Itoa(i) + ".go"
		var head strings.Builder
		for n := 0; n < 20; n++ {
			head.WriteString("line body here\n")
		}
		structure = append(structure, StructureCandidate{
			RelPath: "pkg/" + name, Kind: StructureKindFile, LineCount: 20, StartLine: 1,
			Head: head.String(),
			Symbols: []StructureSymbol{
				{Kind: "func", Name: "Fn", Line: 5},
			},
			ContentHash: name,
		})
	}
	pack, stats, next := assembleContextPack(context.Background(), noRerank, "how does Fn work", structure, caps, FitEdges{})
	if len(pack.Identity) >= 40 {
		t.Fatalf("identity=%d — multi-path soft cap should leave room for substance", len(pack.Identity))
	}
	if len(pack.Substance) == 0 && stats.DepthAdmits == 0 {
		t.Fatalf("expected some substance under multi-path identity meter; identity=%d skeleton=%d stats=%+v",
			len(pack.Identity), len(pack.Skeleton), stats)
	}
	if len(pack.Gaps) == 0 && len(next) == 0 {
		t.Fatal("expected gaps/next_actions for unadmitted paths")
	}
	est := caps.EstimatePackTokens(pack)
	if est > caps.Pack.InputBudgetTokens {
		t.Fatalf("est %d > budget %d", est, caps.Pack.InputBudgetTokens)
	}
}
