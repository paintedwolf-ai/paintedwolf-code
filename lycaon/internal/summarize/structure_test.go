package summarize

import (
	"context"
	"fmt"
	"testing"
)

func structureCandidates(n int, lineCount int) []StructureCandidate {
	out := make([]StructureCandidate, n)
	for i := range out {
		rel := fmt.Sprintf("pkg/f%d.go", i)
		out[i] = StructureCandidate{
			RelPath: rel, Kind: StructureKindFile, LineCount: lineCount,
			Head: "package pkg\n", StartLine: 1,
			Symbols:     []StructureSymbol{{Kind: "func", Name: "Main", Line: 1}},
			ContentHash: HashString(rel),
		}
	}
	return out
}

func TestFillReturnsPackNoLLM(t *testing.T) {
	structure := structureCandidates(40, 50)
	eng := NewEngine(fakeGather{GatherResult{
		Mode: ModeRepo, Structure: structure,
		Stats: GatherStats{Mode: ModeRepo, Candidates: 40, PathIsDir: true, UseStructure: true},
	}}, DefaultCaps())

	res, err := eng.Run(context.Background(), Request{Task: "explain pkg", MaxAnchors: 12})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Pack.Skeleton) == 0 {
		t.Fatal("expected pack skeleton from structure candidates")
	}
	if len(res.Anchors) == 0 {
		t.Fatal("expected host-derived anchors from skeleton symbols")
	}
}

func TestFillBudgetTruncates(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 80
	caps.Pack.SizeDivisor = 1
	structure := structureCandidates(10, 50)
	eng := NewEngine(fakeGather{GatherResult{
		Mode: ModeRepo, Structure: structure,
		Stats: GatherStats{Mode: ModeRepo, Candidates: 10, PathIsDir: true, UseStructure: true},
	}}, caps)

	res, err := eng.Run(context.Background(), Request{Task: "explain", MaxAnchors: 12})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Coverage.Complete {
		t.Fatalf("coverage = %+v, want complete", res.Coverage)
	}
	if res.Orchestration.Curator.PrimaryLimitReason != "pack_budget" {
		t.Fatalf("curator = %+v, want primary_limit_reason=pack_budget", res.Orchestration.Curator)
	}
	foundLeftover := false
	for _, na := range res.NextActions {
		if na.Tool == "read" && na.Path != "" && na.Lines != "" {
			foundLeftover = true
			break
		}
	}
	if !foundLeftover {
		t.Fatalf("next_actions = %+v, want leftover budget reads", res.NextActions)
	}
}

func TestStructureGatherDefaultDir(t *testing.T) {
	structure := structureCandidates(40, 50)
	eng := NewEngine(fakeGather{GatherResult{
		Mode: ModeRepo, Structure: structure,
		Stats: GatherStats{Mode: ModeRepo, Candidates: 40, PathIsDir: true, UseStructure: true},
	}}, DefaultCaps())

	res, err := eng.Run(context.Background(), Request{Task: "explain pkg", MaxAnchors: 12})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Pack.Skeleton) == 0 {
		t.Fatal("expected pack skeleton")
	}
}

func TestStructureCoverageEmitted(t *testing.T) {
	structure := structureCandidates(3, 20)
	eng := NewEngine(fakeGather{GatherResult{
		Mode: ModeRepo, Structure: structure,
		Stats: GatherStats{Mode: ModeRepo, Candidates: 3, UseStructure: true},
	}}, DefaultCaps())

	res, err := eng.Run(context.Background(), Request{Task: "explain", MaxAnchors: 12})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Coverage.Complete {
		t.Fatalf("coverage = %+v", res.Coverage)
	}
}

func TestEnsurePatternCoverageNextAction(t *testing.T) {
	req := Request{Path: "pkg/server.go", Pattern: "handle[A-Z]"}
	got := ensurePatternCoverageNextAction(nil, req, GatherResult{MatchCount: 30}, 12)
	if len(got) != 1 || got[0].Tool != "grep" || got[0].Pattern != "handle[A-Z]" {
		t.Fatalf("next_actions = %+v, want prepended grep", got)
	}
	if got[0].Path != req.Path {
		t.Fatalf("pattern action changed scope: %+v", got[0])
	}
	existing := []NextAction{{Tool: "grep", Path: "pkg/server.go", Pattern: "handle[A-Z]", Why: "existing"}}
	got = ensurePatternCoverageNextAction(existing, req, GatherResult{MatchCount: 30}, 12)
	if len(got) != 1 || got[0].Why != "existing" {
		t.Fatalf("should not duplicate grep: %+v", got)
	}
	if got := ensurePatternCoverageNextAction(nil, req, GatherResult{MatchCount: 10}, 12); len(got) != 0 {
		t.Fatalf("fully covered should stay empty: %+v", got)
	}
}

func TestPatternMatchCountBecomesTotal(t *testing.T) {
	structure := []StructureCandidate{{
		RelPath: "pkg/routes.go", Kind: StructureKindFile, LineCount: 40,
		Head: "package main\nhandleA()\nhandleB()\n", StartLine: 1,
		Symbols: []StructureSymbol{
			{Kind: "match", Name: "handleA()", Line: 2},
			{Kind: "match", Name: "handleB()", Line: 3},
		},
		ContentHash: "h1",
	}}
	g := fakeGather{res: GatherResult{
		Mode: ModeRepo, Structure: structure, MatchCount: 30,
		Stats: GatherStats{Mode: ModeRepo, Candidates: 1, HasPattern: true, UseStructure: true},
	}}
	eng := NewEngine(g, DefaultCaps())
	res, err := eng.Run(context.Background(), Request{
		Task: "list handlers", Path: "pkg/routes.go", Pattern: "handle", MaxAnchors: 12,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Coverage.MatchesObserved != 30 {
		t.Fatalf("matches_total = %d, want 30", res.Coverage.MatchesObserved)
	}
	if res.Gather.MatchCount != 30 {
		t.Fatalf("Gather.MatchCount = %d", res.Gather.MatchCount)
	}
	if len(res.NextActions) == 0 || res.NextActions[0].Tool != "grep" {
		t.Fatalf("next_actions = %+v, want leading grep for uncovered hits", res.NextActions)
	}
}

func TestPatternContinuationPreservesScope(t *testing.T) {
	req := Request{Path: ".", Pattern: "Needle"}
	actions := ensurePatternContinuation(nil, req, GatherResult{
		CatalogRevision: 7, NextCursorPath: "src/next.txt", MatchCount: 13, MatchingFilesObserved: 4,
	})
	if len(actions) != 1 || actions[0].Pattern != req.Pattern || actions[0].Path != req.Path {
		t.Fatalf("actions = %+v", actions)
	}
	state, err := summarizeCursors.Decode(actions[0].Cursor, cursorScope(req))
	if err != nil || state.Revision != 7 || state.Scope != cursorScope(req) || state.Position != "src/next.txt" ||
		state.MatchesObserved != 13 || state.MatchingFilesObserved != 4 {
		t.Fatalf("cursor = %+v %v", state, err)
	}
}
