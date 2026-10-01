package summarize

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type fakeGather struct {
	res GatherResult
}

func (f fakeGather) Gather(_ context.Context, _ Request) (GatherResult, error) {
	return f.res, nil
}

func fileCandidates(n int) []Candidate {
	cands := make([]Candidate, n)
	for i := range cands {
		rel := fmt.Sprintf("pkg/f%d.go", i)
		cands[i] = Candidate{
			RelPath:     rel,
			Kind:        KindFile,
			ContentHash: HashString(rel),
			Body:        "package pkg",
		}
	}
	return cands
}

func TestRunSingleReturnsPack(t *testing.T) {
	cands := fileCandidates(3)
	eng := NewEngine(fakeGather{GatherResult{
		Mode: ModeRepo, Candidates: cands,
		Stats: GatherStats{Mode: ModeRepo, Candidates: 3, PathIsFile: true},
	}}, DefaultCaps())

	res, err := eng.Run(context.Background(), Request{Task: "what is here", MaxAnchors: 12})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Pack.Identity) == 0 {
		t.Fatal("expected pack identity from gathered candidates")
	}
}

func TestRunColdStructureReturnsPack(t *testing.T) {
	structure := structureCandidates(4, 50)
	eng := NewEngine(fakeGather{GatherResult{
		Mode: ModeRepo, Structure: structure,
		Stats: GatherStats{Mode: ModeRepo, Candidates: 4, PathIsDir: true, UseStructure: true},
	}}, DefaultCaps())

	res, err := eng.Run(context.Background(), Request{Task: "explain", MaxAnchors: 12})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Pack.Skeleton) == 0 {
		t.Fatal("expected pack skeleton from structure candidates")
	}
}

func TestRunNoMaterial(t *testing.T) {
	eng := NewEngine(fakeGather{GatherResult{Mode: ModeRepo}}, DefaultCaps())
	if _, err := eng.Run(context.Background(), Request{Task: "x"}); !errors.Is(err, ErrNoMaterial) {
		t.Fatalf("err = %v, want ErrNoMaterial", err)
	}
}

func TestRunRejectsCursorFromAnotherPattern(t *testing.T) {
	req := Request{Path: ".", Pattern: "two"}
	req.Cursor = encodeCursor(7, cursorScope(Request{Path: ".", Pattern: "one"}), "src/a.txt")
	eng := NewEngine(fakeGather{}, DefaultCaps())
	if _, err := eng.Run(context.Background(), req); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("error = %v", err)
	}
}

func TestClampAnchors(t *testing.T) {
	caps := DefaultCaps()
	if got := caps.ClampAnchors(0); got != 12 {
		t.Fatalf("omitted = %d, want default 12", got)
	}
	if got := caps.ClampAnchors(100); got != 24 {
		t.Fatalf("over = %d, want max 24", got)
	}
	if got := caps.ClampAnchors(5); got != 5 {
		t.Fatalf("in-range = %d, want 5", got)
	}
}
