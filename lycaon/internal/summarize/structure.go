package summarize

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/observability"
)

func structureRankBody(c StructureCandidate) string {
	var b strings.Builder
	for _, s := range c.Symbols {
		fmt.Fprintf(&b, "%s %s ", s.Kind, s.Name)
	}
	if c.Head != "" {
		b.WriteString(c.Head)
	}
	for _, t := range c.RollupRows {
		b.WriteString(" ")
		b.WriteString(t)
	}
	return b.String()
}

// fillResult holds the assembled pack and its derived navigation.
type fillResult struct {
	Pack        ContextPack
	Anchors     []Anchor
	NextActions []NextAction
	Curator     CuratorStats
}

// executeFill assembles breadth, depth, and fit tiers.
func (e *Engine) executeFill(ctx context.Context, req Request, structure []StructureCandidate, fit FitEdges, subtree *SubtreeNode, importance SubtreeImportance, packBudget int) fillResult {
	structure = rankStructureCandidates(ctx, e.Rerank, req.Task, structure)
	caps := e.Caps
	if packBudget > 0 {
		caps.Pack.InputBudgetTokens = packBudget
	}

	var pack ContextPack
	var stats CuratorStats
	var leftoverActions []NextAction
	if subtree != nil && (len(subtree.Children) > 0 || subtree.Remainder != nil) {
		pack, stats, leftoverActions = allocateContextPack(ctx, e.Rerank, req.Task, subtree, structure, caps, fit, importance, e.Outliner)
		e.applySubstanceFloor(ctx, req.Task, caps, subtree, importance, &pack, &stats)
	} else {
		pack, stats, leftoverActions = assembleContextPack(ctx, e.Rerank, req.Task, structure, caps, fit)
	}

	return fillResult{
		Pack:        pack,
		Anchors:     dedupeAnchors(pickPackAnchors(pack, e.Caps, req.MaxAnchors)),
		NextActions: finalizeNextActions(ctx, e.Rerank, req.Task, leftoverActions, pack, e.Caps),
		Curator:     stats,
	}
}

// pickPackAnchors cites ranked symbols from admitted source windows.
func pickPackAnchors(pack ContextPack, caps Caps, maxAnchors int) []Anchor {
	if maxAnchors <= 0 {
		maxAnchors = caps.Anchors.Default
	}
	if maxAnchors <= 0 {
		maxAnchors = 8
	}
	var out []Anchor
	for _, w := range pack.Substance {
		if anchor, ok := windowAnchor(w, pack.Skeleton); ok {
			out = append(out, anchor)
		}
		if len(out) >= maxAnchors {
			break
		}
	}
	return out
}

func windowAnchor(window PackWindow, skeleton []PackSymbol) (Anchor, bool) {
	anchorAt := func(line int) (Anchor, bool) {
		excerpt := evidence.StripNumberedLinePrefix(numberedWindowLine(window.Body, line))
		return Anchor{Path: window.Path, Line: line, Excerpt: excerpt}, line > 0 && usableAnchorExcerpt(excerpt)
	}
	for _, symbol := range skeleton {
		if symbol.Path == window.Path && symbol.Line >= window.StartLine && symbol.Line <= window.EndLine {
			if anchor, ok := anchorAt(symbol.Line); ok {
				return anchor, true
			}
		}
	}
	for line := window.StartLine; line <= window.EndLine; line++ {
		if anchor, ok := anchorAt(line); ok {
			return anchor, true
		}
	}
	return Anchor{}, false
}

// numberedWindowLine returns the "N: …" row for line from a numberWindowBody.
func numberedWindowLine(body string, line int) string {
	if body == "" || line <= 0 {
		return ""
	}
	prefix := fmt.Sprintf("%d:", line)
	for _, row := range strings.Split(body, "\n") {
		t := strings.TrimSpace(row)
		if strings.HasPrefix(t, prefix) {
			return t
		}
	}
	return ""
}

func usableAnchorExcerpt(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return true
		}
	}
	return false
}

func structureSources(structure []StructureCandidate) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range structure {
		if s.RelPath == "" || s.Kind == StructureKindDirMap || seen[s.RelPath] {
			continue
		}
		if strings.Contains(s.RelPath, "#escalation") {
			continue
		}
		seen[s.RelPath] = true
		out = append(out, s.RelPath)
	}
	return out
}

func (e *Engine) runFill(ctx context.Context, req Request, gr GatherResult, packBudget int) (Result, error) {
	// Pattern queries use the configured maximum anchor count.
	if strings.TrimSpace(req.Pattern) != "" && req.MaxAnchors < e.Caps.Anchors.Max {
		req.MaxAnchors = e.Caps.ClampAnchors(e.Caps.Anchors.Max)
	}

	structure := append([]StructureCandidate(nil), gr.Structure...)
	for _, c := range gr.Candidates {
		kind := StructureKindFile
		if c.Kind == KindInline || strings.HasPrefix(c.RelPath, "inline") {
			kind = StructureKindInline
		}
		structure = append(structure, StructureCandidate{
			RelPath: c.RelPath, Kind: kind,
			Head: c.Body, StartLine: c.StartLine, LineCount: strings.Count(c.Body, "\n") + 1,
			ContentHash: c.ContentHash,
		})
	}
	subtree := gr.Subtree
	if subtree != nil {
		subtree = CloneSubtree(subtree)
		stampCursorScope(subtree, cursorScope(req))
		if strings.TrimSpace(req.CursorPosition) != "" {
			subtree.Cursor = strings.TrimSpace(req.CursorPosition)
		}
	}
	fr := e.executeFill(ctx, req, structure, gr.Fit, subtree, gr.Importance, packBudget)
	nextActions := ensurePatternCoverageNextAction(fr.NextActions, req, gr, len(fr.Anchors))
	nextActions = ensurePatternContinuation(nextActions, req, gr)
	if subtree != nil && subtree.Remainder != nil && subtree.Remainder.Next != "" {
		action := NextAction{Tool: "summarize", Path: req.Path, Paths: req.Paths, Task: req.Task, Cursor: encodeCursor(gr.CatalogRevision, cursorScope(req), subtree.Remainder.Next), Why: "continue directory coverage"}
		nextActions = append([]NextAction{action}, nextActions...)
	}
	nextActions = clampNextActions(append(nextActions, gr.NextActions...), e.Caps)
	if gr.CatalogState != "" && gr.CatalogState != "ready" {
		fr.Pack.Gaps = append(fr.Pack.Gaps, "Repository indexing is in progress; directory totals are not yet available.")
	}

	if observability.SummarizeDebugEnabled() {
		omitted := citedFileOmission(gr.Candidates)
		observability.LogSummarizeCall(observability.SummarizeDebugCapture{
			Task: req.Task, Mode: string(gr.Mode),
			MaxAnchors:        req.MaxAnchors,
			CitedFileOmission: len(omitted) > 0, OmittedPaths: omitted,
		})
	}

	coverage := summarizeCoverage(req, gr, subtree, fr, nextActions)

	return Result{
		Task:        req.Task,
		Pack:        fr.Pack,
		Anchors:     fr.Anchors,
		NextActions: nextActions,
		Coverage:    coverage,
		Sources:     resultSources(structure, fr.Pack),
		Gather: GatherReport{
			Mode: gr.Mode, Path: req.Path, Paths: req.Paths, Pattern: req.Pattern,
			Candidates: len(structure), Bytes: gr.Bytes, MatchCount: gr.MatchCount,
			SampleMatches: gr.SampleMatches,
		},
		Orchestration: OrchestrationReport{
			Curator: mergeGatherCuratorStats(fr.Curator, gr.Stats),
		},
	}, nil
}

func stampCursorScope(root *SubtreeNode, scope string) {
	if root == nil {
		return
	}
	root.CursorScope = scope
	for _, child := range root.Children {
		stampCursorScope(child, scope)
	}
}

func summarizeCoverage(req Request, gr GatherResult, subtree *SubtreeNode, fr fillResult, actions []NextAction) Coverage {
	coverage := Coverage{
		CatalogState: gr.CatalogState, CatalogRefreshing: gr.CatalogRefreshing,
		Complete: true, CatalogRevision: gr.CatalogRevision,
		AnchorsReturned: len(fr.Anchors), MatchesObserved: gr.MatchCount,
		MatchSamplesReturned: len(gr.SampleMatches), Cursor: req.Cursor,
	}
	if gr.NextCursorPath != "" || gr.CatalogState != "" && gr.CatalogState != "ready" {
		coverage.Complete = false
	}
	if subtree != nil {
		coverage.FilesTotal = subtree.Material.SourceFiles
		coverage.FilesRepresented = subtree.Material.SourceFiles
		coverage.DefinitionsTotal = subtree.Material.Defs
		coverage.ChildrenTotal = len(subtree.Children)
		if subtree.LoadChildren != nil {
			coverage.ChildrenTotal = subtree.ChildCount
		}
		if subtree.UnknownMaterial {
			coverage.Complete = false
			coverage.FilesTotal = 0
			coverage.DefinitionsTotal = 0
			coverage.ChildrenTotal = 0
		}
		visible := childrenFromCursor(subtree.Children, req.CursorPosition)
		coverage.ChildrenReturned = representedChildCount(visible, fr.Pack)
	} else {
		for _, candidate := range gr.Structure {
			if candidate.Kind == StructureKindFile {
				coverage.FilesTotal++
			}
			coverage.DefinitionsTotal += len(candidate.Symbols)
		}
		coverage.FilesRepresented = coverage.FilesTotal
		if strings.TrimSpace(req.Pattern) != "" {
			coverage.FilesTotal = 0
			coverage.MatchingFilesObserved = gr.MatchingFilesObserved
		}
	}
	for _, action := range actions {
		if action.Tool == "summarize" && action.Cursor != "" {
			if cursor, ok := decodeCursor(action.Cursor); ok && cursor.Scope == cursorScope(req) {
				coverage.NextCursor = action.Cursor
				break
			}
		}
	}
	return coverage
}

func ensurePatternContinuation(actions []NextAction, req Request, gr GatherResult) []NextAction {
	if strings.TrimSpace(req.Pattern) == "" || gr.NextCursorPath == "" {
		return actions
	}
	cursor := encodePatternCursor(
		gr.CatalogRevision, cursorScope(req), gr.NextCursorPath,
		gr.MatchCount, gr.MatchingFilesObserved,
	)
	if cursor == "" {
		return actions
	}
	for _, action := range actions {
		if action.Tool == "summarize" && action.Cursor == cursor {
			return actions
		}
	}
	action := NextAction{
		Tool: "summarize", Path: req.Path, Paths: append([]string(nil), req.Paths...),
		Task: req.Task, Pattern: req.Pattern, Cursor: cursor, Why: "continue the pattern briefing",
	}
	return append([]NextAction{action}, actions...)
}

func representedChildCount(children []*SubtreeNode, pack ContextPack) int {
	represented := make(map[string]bool, len(children))
	mark := func(candidate string) {
		for _, child := range children {
			if child == nil || child.Path == "" {
				continue
			}
			if candidate == child.Path || strings.HasPrefix(candidate, strings.TrimSuffix(child.Path, "/")+"/") {
				represented[child.Path] = true
				return
			}
		}
	}
	for _, identity := range pack.Identity {
		mark(identity.Path)
	}
	for _, symbol := range pack.Skeleton {
		mark(symbol.Path)
	}
	return len(represented)
}

func resultSources(structure []StructureCandidate, pack ContextPack) []string {
	out := structureSources(structure)
	seen := make(map[string]bool, len(out))
	for _, source := range out {
		seen[source] = true
	}
	for _, identity := range pack.Identity {
		if identity.Path == "" || seen[identity.Path] {
			continue
		}
		seen[identity.Path] = true
		out = append(out, identity.Path)
	}
	return out
}

// ensurePatternCoverageNextAction offers exact search in each requested scope.
func ensurePatternCoverageNextAction(actions []NextAction, req Request, gr GatherResult, anchorCount int) []NextAction {
	pattern := strings.TrimSpace(req.Pattern)
	if pattern == "" || gr.NextCursorPath == "" && gr.MatchCount <= anchorCount {
		return actions
	}
	paths := append([]string(nil), req.Paths...)
	if req.Path != "" {
		paths = append([]string{req.Path}, paths...)
	}
	seen := make(map[string]bool)
	for _, action := range actions {
		if action.Tool == "grep" && action.Pattern == pattern {
			seen[action.Path] = true
		}
	}
	var searches []NextAction
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		searches = append(searches, NextAction{Tool: "grep", Path: path, Pattern: pattern, Why: "Inspect matching lines"})
	}
	return append(searches, actions...)
}

// clampNextActions preserves rank order.
func clampNextActions(actions []NextAction, caps Caps) []NextAction {
	maxN := caps.Pack.NextActionsMax
	if maxN <= 0 {
		maxN = DefaultCaps().Pack.NextActionsMax
	}
	if maxN <= 0 || len(actions) <= maxN {
		return actions
	}
	return actions[:maxN]
}

func mergeGatherCuratorStats(curator CuratorStats, stats GatherStats) CuratorStats {
	curator.NestedReposPruned = stats.NestedReposPruned
	curator.FaninGrepPasses = stats.FaninGrepPasses
	return curator
}
