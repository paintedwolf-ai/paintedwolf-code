package summarize

import (
	"strings"
	"testing"
)

func TestCursorBindsRevisionAndChild(t *testing.T) {
	scope := cursorScope(Request{Path: "src", Pattern: "Needle"})
	cursor := encodeCursor(42, scope, "src/next")
	state, ok := decodeCursor(cursor)
	if !ok || state.Revision != 42 || state.Scope != scope || state.Position != "src/next" {
		t.Fatalf("decode = %+v %v", state, ok)
	}
	if _, ok := decodeCursor("src/next"); ok {
		t.Fatal("plain path accepted as cursor")
	}
	parts := strings.Split(cursor, ".")
	replacement := byte('A')
	if parts[1][0] == replacement {
		replacement = 'B'
	}
	parts[1] = string(replacement) + parts[1][1:]
	tampered := strings.Join(parts, ".")
	if _, ok := decodeCursor(tampered); ok {
		t.Fatal("tampered cursor accepted")
	}
}

func TestPatternCursorCarriesExactTotals(t *testing.T) {
	scope := cursorScope(Request{Path: ".", Pattern: "Needle"})
	state, ok := decodeCursor(encodePatternCursor(8, scope, "src/next", 41, 12))
	if !ok || state.MatchesObserved != 41 || state.MatchingFilesObserved != 12 {
		t.Fatalf("decode = %+v %v", state, ok)
	}
}

func TestCursorScopeBindsPatternAndPathSet(t *testing.T) {
	a := cursorScope(Request{Paths: []string{"b", "a"}, Pattern: "one"})
	b := cursorScope(Request{Paths: []string{"a", "b"}, Pattern: "one"})
	if a != b {
		t.Fatal("path order changed cursor scope")
	}
	if a == cursorScope(Request{Paths: []string{"a", "b"}, Pattern: "two"}) {
		t.Fatal("pattern did not change cursor scope")
	}
}

func TestCoverageCursorBelongsToRequestedScope(t *testing.T) {
	req := Request{Path: ".", Task: "understand source"}
	child := Request{Path: "src", Task: req.Task}
	childCursor := encodeCursor(1, cursorScope(child), "child-page")
	actions := []NextAction{{Tool: "summarize", Path: child.Path, Task: child.Task, Cursor: childCursor}}
	coverage := summarizeCoverage(req, GatherResult{}, nil, fillResult{}, actions)
	if coverage.NextCursor != "" {
		t.Fatal("root coverage advertised a nested directory cursor")
	}
	rootCursor := encodeCursor(1, cursorScope(req), "root-page")
	actions = append(actions, NextAction{Tool: "summarize", Path: req.Path, Task: req.Task, Cursor: rootCursor})
	coverage = summarizeCoverage(req, GatherResult{}, nil, fillResult{}, actions)
	if coverage.NextCursor != rootCursor {
		t.Fatal("root coverage lost its continuation")
	}
}

func TestCoverageOmitsUnknownTotalsAcrossMultipleTargets(t *testing.T) {
	root := &SubtreeNode{Kind: SubtreeKindDir, Children: []*SubtreeNode{
		{Path: "ready", Kind: SubtreeKindDir, Material: Material{SourceFiles: 10}},
		{Path: "warming", Kind: SubtreeKindDir, UnknownMaterial: true},
	}}
	SumMaterialBottomUp(root)
	coverage := summarizeCoverage(Request{Paths: []string{"ready", "warming"}}, GatherResult{}, root, fillResult{}, nil)
	if coverage.Complete || coverage.FilesTotal != 0 || !root.UnknownMaterial {
		t.Fatalf("unknown union presented as exact: %+v", coverage)
	}
}
