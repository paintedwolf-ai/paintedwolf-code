package survey

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

type patternPosition struct {
	Target int    `json:"target"`
	After  string `json:"after"`
}

type patternScope struct {
	resolved    projectpaths.Resolved
	reader      *sourcecatalog.SummaryReader
	catalogRoot string
	base        string
	file        bool
}

// patternScopes pins generations before any source work, so continuations bind
// both permission scope and every requested root, including not-yet-read roots.
func (g *summarizeGatherer) patternScopes(ctx context.Context, roots []string) ([]patternScope, error) {
	var out []patternScope
	ordered := append([]string(nil), roots...)
	sort.Strings(ordered)
	digest := sha256.New()
	for _, target := range ordered {
		resolved, err := projectpaths.ResolveRead(ctx, g.boundary, g.tctx, target)
		if err != nil {
			return nil, err
		}
		duplicate := false
		for _, existing := range out {
			if resolved.Abs == existing.resolved.Abs || !existing.file && strings.HasPrefix(resolved.Abs, existing.resolved.Abs+string(filepath.Separator)) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		info, err := os.Stat(resolved.Abs)
		if err != nil {
			return nil, err
		}
		scope, err := g.boundary.CompileReadScope(ctx, resolved.Root.Path, g.tctx.ProfileID())
		if err != nil {
			return nil, err
		}
		view := patternScope{resolved: resolved, catalogRoot: resolved.Root.Path, base: projectroot.ScopeRel(resolved.Root, resolved.Abs), file: !info.IsDir()}
		fmt.Fprintf(digest, "%s\x00%s\x00", resolved.Abs, scope.Key)
		if view.file {
			fmt.Fprintf(digest, "%d:%d;", info.Size(), info.ModTime().UnixNano())
		} else {
			wait := time.Duration(0)
			if !g.indexWaitUsed {
				wait = time.Duration(g.caps.Gather.IndexWaitMs) * time.Millisecond
				g.indexWaitUsed = true
			}
			reader, status, err := g.patternDirectoryView(ctx, &view, scope, wait)
			if err != nil {
				return nil, err
			}
			if status.State == sourcecatalog.StateFailed {
				return nil, fmt.Errorf("source catalog: %s", status.Error)
			}
			if reader == nil {
				g.treeState = status.State
			} else if g.treeState == "" {
				g.treeState = sourcecatalog.StateReady
			}
			g.treeRefreshing = g.treeRefreshing || status.Refreshing
			view.reader = reader
			fmt.Fprintf(digest, "%d;", status.Revision)
		}
		out = append(out, view)
	}
	g.catalogRevision = max(1, binary.BigEndian.Uint64(digest.Sum(nil)[:8])>>11)
	return out, nil
}

func (g *summarizeGatherer) probePatternPage(ctx context.Context, req summarize.Request, roots []string, pageSize int) (summarizePatternProbe, error) {
	result := summarizePatternProbe{MatchCount: req.CursorMatchesObserved, MatchingFiles: req.CursorMatchingFilesObserved}
	opts, err := parseGrepArgs(map[string]any{"pattern": req.Pattern, "structural": false})
	if err != nil {
		return result, err
	}
	re, err := compileGrepPattern(opts)
	if err != nil {
		return result, err
	}
	scopes, err := g.patternScopes(ctx, roots)
	if err != nil {
		return result, err
	}
	result.Revision = g.catalogRevision
	position := patternPosition{}
	if req.CursorPosition != "" {
		if json.Unmarshal([]byte(req.CursorPosition), &position) != nil || position.Target < 0 || position.Target >= len(scopes) {
			return result, summarize.ErrInvalidCursor
		}
		result.CursorFound = true
	}
	require := litprefilter.Extract(req.Pattern, false)
	drafts := sourceview.DraftsFor(ctx, g.tctx)
	resume := func() { raw, _ := surveyjson.Marshal(position); result.NextPath = string(raw) }
	for position.Target < len(scopes) {
		scope := scopes[position.Target]
		candidates, more, pageErr := g.patternPageCandidates(ctx, scope, position.After, pageSize)
		if pageErr != nil {
			return result, pageErr
		}
		for _, candidate := range candidates {
			if g.sourceBudgetExhausted() || len(result.Matches) >= pageSize {
				resume()
				return result, nil
			}
			abs := filepath.Join(scope.catalogRoot, filepath.FromSlash(candidate.path))
			display := projectpaths.QualifyAbs(g.tctx, scope.resolved.Root, abs)

			if !require.Empty() && g.catalog != nil && candidate.entry.Size > 0 {
				if _, hasDraft := drafts.Lookup(abs); !hasDraft {
					if g.catalog.CanPrune(scope.catalogRoot, require, candidate.entry) {
						position.After = candidate.path
						continue
					}
				}
			}

			raw, readErr := g.patternSource(ctx, abs)
			if errors.Is(readErr, errSummaryReadBudget) {
				resume()
				return result, nil
			}
			position.After = candidate.path
			if readErr != nil {
				if errors.Is(readErr, errUnsupportedSummaryText) {
					g.noteNonTextPath(display)
				}
				if errors.Is(readErr, errUnsupportedSummaryText) || os.IsNotExist(readErr) || os.IsPermission(readErr) {
					result.SkippedPaths = append(result.SkippedPaths, display)
					continue
				}
				return result, readErr
			}
			matches, total, scanErr := scanSummarizePatternContent(ctx, raw, display, re, 1)
			if errors.Is(scanErr, errSummaryPatternNonText) {
				g.noteNonTextPath(display)
				result.SkippedPaths = append(result.SkippedPaths, display)
				continue
			}
			if scanErr != nil {
				return result, scanErr
			}
			result.MatchCount += total
			if total > 0 {
				result.MatchingFiles++
				result.Matches = append(result.Matches, matches...)
			}
		}
		if more {
			resume()
			return result, nil
		}
		position.Target++
		position.After = ""
	}
	return result, nil
}

type summarizePatternCandidate struct {
	path  string
	entry sourcecatalog.Entry
}

func (g *summarizeGatherer) patternPageCandidates(ctx context.Context, scope patternScope, after string, limit int) ([]summarizePatternCandidate, bool, error) {
	if scope.file {
		if after != "" {
			return nil, false, nil
		}
		return []summarizePatternCandidate{{path: scope.base}}, false, nil
	}
	if scope.reader == nil {
		// Bounded live children supply useful material while the shared index warms.
		node := g.warmingSubtree(ctx, scope.resolved)
		if g.treeErr != nil {
			return nil, false, g.treeErr
		}
		var paths []string
		frontier := []*summarize.SubtreeNode{node}
		for len(frontier) > 0 && len(paths) < limit {
			current := frontier[0]
			frontier = frontier[1:]
			if current == nil {
				continue
			}
			if current.LoadChildren != nil {
				current.LoadChildren(ctx, current)
				if g.treeErr != nil {
					return nil, false, g.treeErr
				}
			}
			for _, child := range current.Children {
				if child.RollupOnly {
					continue
				}
				if child.Kind == summarize.SubtreeKindDir {
					frontier = append(frontier, child)
					continue
				}
				resolved, err := projectpaths.ResolveRead(ctx, g.boundary, g.tctx, child.Path)
				if err != nil {
					continue
				}
				rel, err := filepath.Rel(scope.catalogRoot, resolved.Abs)
				if err == nil && filepath.ToSlash(rel) > after {
					paths = append(paths, filepath.ToSlash(rel))
				}
				if len(paths) == limit {
					break
				}
			}
		}

		sort.Strings(paths)
		out := make([]summarizePatternCandidate, len(paths))
		for i, p := range paths {
			out[i] = summarizePatternCandidate{path: p}
		}
		return out, false, nil
	}
	nodes, more, err := scope.reader.FilesPage(ctx, scope.base, after, limit)
	candidates := make([]summarizePatternCandidate, 0, len(nodes))
	for _, node := range nodes {
		if os.FileMode(node.Mode).IsRegular() {
			candidates = append(candidates, summarizePatternCandidate{path: node.Path, entry: node.Entry})
		}
	}
	return candidates, more, err
}

// Discovery, pattern matching and outlining share one source reservation and cache.
func (g *summarizeGatherer) patternSource(ctx context.Context, abs string) ([]byte, error) {
	raw, err := g.readFileCached(ctx, abs)
	if !errors.Is(err, errFileNeedsStreaming) {
		return raw, err
	}
	err = g.scanSummaryChunks(ctx, abs, g.caps.Gather.FileChunkBytes, func(_ int, part []byte) error { raw = append(raw, part...); return nil })
	if err == nil {
		g.storeBytes(abs, raw)
	}
	return raw, err
}

func (g *summarizeGatherer) patternDirectoryView(ctx context.Context, view *patternScope, scope sandbox.CompiledReadScope, wait time.Duration) (*sourcecatalog.SummaryReader, sourcecatalog.TreeStatus, error) {
	root := view.resolved.Root
	reader, status, err := g.openSummaryTree(ctx, sourcecatalog.Root{ID: root.ID, Path: root.Path}, sourcecatalog.TreeScope{Key: scope.Key, Filter: scope.Filter, PruneNestedVCS: g.caps.Gather.PruneNestedVCS}, wait)
	if err != nil || reader == nil || view.base == "." || !g.caps.Gather.PruneNestedVCS {
		return reader, status, err
	}
	node, err := reader.Node(ctx, view.base)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, status, err
	}
	if err == nil && !node.Boundary {
		return reader, status, nil
	}
	base := view.base
	filter := func(rel string, isDir bool) bool {
		return scope.Filter == nil || scope.Filter(path.Join(base, rel), isDir)
	}
	view.catalogRoot = view.resolved.Abs
	view.base = "."
	return g.openSummaryTree(ctx, sourcecatalog.Root{ID: root.ID + ":" + base, Path: view.resolved.Abs, Within: view.resolved.Root.Path}, sourcecatalog.TreeScope{Key: scope.Key, Filter: filter, PruneNestedVCS: true}, 0)
}

func (g *summarizeGatherer) patternInspectionActions(ctx context.Context, req summarize.Request, actions []summarize.NextAction) []summarize.NextAction {
	if req.Pattern == "" {
		return actions
	}
	paths := make([]string, 0, len(g.truncatedPaths))
	for abs := range g.truncatedPaths {
		paths = append(paths, abs)
	}
	sort.Strings(paths)
	for _, abs := range paths {
		if len(actions) >= g.caps.Pack.NextActionsMax {
			break
		}
		resolved, err := projectpaths.ResolveRead(ctx, g.boundary, g.tctx, abs)
		if err != nil {
			continue
		}
		actions = append(actions, summarize.NextAction{Tool: "grep", Path: resolved.DisplayPath, Pattern: req.Pattern, Why: "Inspect matching lines"})
	}
	return actions
}
