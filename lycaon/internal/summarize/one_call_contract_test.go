package summarize

import (
	"context"
	"strings"
	"testing"
)

type oneCallWant struct {
	budgetOK bool
	gaps     bool
}

type oneCallCase struct {
	name string
	prep func(t *testing.T) (*Engine, Caps)
	req  Request
	want oneCallWant
}

// TestOneCallContract checks pack budgets across gather shapes.
func TestOneCallContract(t *testing.T) {
	t.Parallel()
	for _, tc := range oneCallCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertOneCallCase(t, tc)
		})
	}
}

func oneCallCases() []oneCallCase {
	out := oneCallFileCases()
	out = append(out, oneCallDirCases()...)
	out = append(out, oneCallMiscCases()...)
	return out
}

func oneCallFileCases() []oneCallCase {
	return []oneCallCase{
		{
			name: "file",
			prep: func(t *testing.T) (*Engine, Caps) {
				caps := DefaultCaps()
				structure := structureCandidates(1, 40)
				structure[0].RelPath = "pkg/a.go"
				eng := NewEngine(fakeGather{GatherResult{
					Mode: ModeRepo, Structure: structure,
					Stats: GatherStats{Mode: ModeRepo, Candidates: 1, PathIsFile: true, UseStructure: true},
				}}, caps)
				return eng, caps
			},
			req:  Request{Task: "explain file", MaxAnchors: 12},
			want: oneCallWant{budgetOK: true},
		},
		{
			name: "large_file_cache_miss",
			prep: func(t *testing.T) (*Engine, Caps) {
				caps := DefaultCaps()
				structure := structureCandidates(1, 500)
				structure[0].RelPath = "pkg/big.go"
				structure[0].ContentHash = "miss-hash"
				eng := NewEngine(fakeGather{GatherResult{
					Mode: ModeRepo, Structure: structure,
					Stats: GatherStats{Mode: ModeRepo, Candidates: 1, PathIsFile: true, UseStructure: true},
				}}, caps)
				return eng, caps
			},
			req:  Request{Task: "explain big", MaxAnchors: 12},
			want: oneCallWant{budgetOK: true},
		},
	}
}

func oneCallDirCases() []oneCallCase {
	return []oneCallCase{
		{
			name: "small_directory",
			prep: func(t *testing.T) (*Engine, Caps) {
				caps := DefaultCaps()
				structure := structureCandidates(5, 30)
				eng := NewEngine(fakeGather{GatherResult{
					Mode: ModeRepo, Structure: structure,
					Stats: GatherStats{Mode: ModeRepo, Candidates: 5, PathIsDir: true, UseStructure: true},
				}}, caps)
				return eng, caps
			},
			req:  Request{Task: "explain dir", MaxAnchors: 12},
			want: oneCallWant{budgetOK: true},
		},
		{
			name: "large_directory_budget",
			prep: func(t *testing.T) (*Engine, Caps) {
				caps := DefaultCaps()
				caps.Pack.InputBudgetTokens = 200
				caps.Pack.SizeDivisor = 4
				structure := structureCandidates(20, 50)
				for i := range structure {
					structure[i].Head = strings.Repeat("body line with content here\n", 50)
					structure[i].StartLine = 1
				}
				eng := NewEngine(fakeGather{GatherResult{
					Mode: ModeRepo, Structure: structure,
					Stats: GatherStats{Mode: ModeRepo, Candidates: 20, PathIsDir: true, UseStructure: true},
				}}, caps)
				return eng, caps
			},
			req:  Request{Task: "explain large dir", MaxAnchors: 12},
			want: oneCallWant{budgetOK: true, gaps: true},
		},
		{
			name: "multi_path",
			prep: func(t *testing.T) (*Engine, Caps) {
				caps := DefaultCaps()
				structure := structureCandidates(3, 40)
				eng := NewEngine(fakeGather{GatherResult{
					Mode: ModeRepo, Structure: structure,
					Stats: GatherStats{Mode: ModeRepo, Candidates: 3, UseStructure: true},
				}}, caps)
				return eng, caps
			},
			req:  Request{Task: "explain paths", Paths: []string{"pkg/f0.go", "pkg/f1.go", "pkg/f2.go"}, MaxAnchors: 12},
			want: oneCallWant{budgetOK: true},
		},
	}
}

func oneCallMiscCases() []oneCallCase {
	return []oneCallCase{
		{
			name: "pattern",
			prep: func(t *testing.T) (*Engine, Caps) {
				caps := DefaultCaps()
				structure := structureCandidates(4, 40)
				eng := NewEngine(fakeGather{GatherResult{
					Mode: ModeRepo, Structure: structure,
					Stats: GatherStats{Mode: ModeRepo, Candidates: 4, HasPattern: true, UseStructure: true},
				}}, caps)
				return eng, caps
			},
			req:  Request{Task: "find matches", Pattern: "func Main", MaxAnchors: 12},
			want: oneCallWant{budgetOK: true},
		},
		{
			name: "inline_parseable",
			prep: func(t *testing.T) (*Engine, Caps) {
				caps := DefaultCaps()
				structure := []StructureCandidate{{
					RelPath: "inline", Kind: StructureKindInline, LineCount: 20,
					Head: "package main\nfunc main() {}\n", StartLine: 1,
					Symbols:     []StructureSymbol{{Kind: "func", Name: "main", Line: 2}},
					ContentHash: HashString("inline"),
				}}
				eng := NewEngine(fakeGather{GatherResult{
					Mode: ModeInline, Structure: structure,
					Stats: GatherStats{Mode: ModeInline, Candidates: 1, UseStructure: true},
				}}, caps)
				return eng, caps
			},
			req:  Request{Task: "explain snippet", Content: "package main\nfunc main() {}\n", MaxAnchors: 12},
			want: oneCallWant{budgetOK: true},
		},
	}
}

func assertOneCallCase(t *testing.T, tc oneCallCase) {
	t.Helper()
	eng, caps := tc.prep(t)
	res, err := eng.Run(context.Background(), tc.req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if tc.want.budgetOK {
		est := caps.EstimatePackTokens(res.Pack)
		if est > caps.Pack.InputBudgetTokens {
			t.Fatalf("pack est_tokens %d exceeds budget %d", est, caps.Pack.InputBudgetTokens)
		}
	}
	if tc.want.gaps {
		assertOneCallGaps(t, res)
	}
}

func assertOneCallGaps(t *testing.T, res Result) {
	t.Helper()
	for _, na := range res.NextActions {
		if na.Tool == "read" && na.Path != "" {
			return
		}
	}
	t.Fatalf("expected gaps or next_actions; next=%+v coverage=%+v", res.NextActions, res.Coverage)
}
