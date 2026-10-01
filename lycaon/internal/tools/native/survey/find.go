package survey

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/pkg/api"
)

// FindTool discovers paths with bounded depth and paginated results.
type FindTool struct {
	Boundary *sandbox.Boundary
	Catalog  *sourcecatalog.Catalog
}

func (t *FindTool) Name() string { return "find" }

type findEntryType string

const (
	findTypeFile findEntryType = "file"
	findTypeDir  findEntryType = "dir"
	findTypeAny  findEntryType = "any"
)

type findResult struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size *int64 `json:"size,omitempty"`
}

type findResponse struct {
	View               string          `json:"view,omitempty"`
	Results            []findResult    `json:"results,omitempty"`
	Offset             int             `json:"offset"`
	TotalResults       int             `json:"total_results,omitempty"`
	Truncated          bool            `json:"truncated"`
	NextOffset         *int            `json:"next_offset,omitempty"`
	MaxDepth           int             `json:"max_depth,omitempty"`
	MaxResults         int             `json:"max_results,omitempty"`
	DeeperPathsOmitted bool            `json:"deeper_paths_omitted,omitempty"`
	DepthNotice        string          `json:"depth_notice,omitempty"`
	Distribution       []findDistEntry `json:"distribution,omitempty"`
	Note               string          `json:"note,omitempty"`
	Highlights         []readHighlight `json:"highlights,omitempty"`
	Gloss              []readGlossLine `json:"gloss,omitempty"`
	Selected           int             `json:"selected,omitempty"`
	Total              int             `json:"total,omitempty"`
	TruncationBanner   string          `json:"truncation_banner,omitempty"`
	Inventory          *inventoryFacts `json:"inventory,omitempty"`
}

func (t *FindTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ctx, _ = withInventoryReport(ctx)
	relRoot := "."
	if raw, ok := args["path"].(string); ok && strings.TrimSpace(raw) != "" {
		relRoot = strings.TrimSpace(raw)
	}
	if err := safecmd.RejectPathEscape(relRoot); err != nil {
		return "", err
	}
	roots, err := projectpaths.UnionDiscoveryRoots(ctx, tctx, relRoot)
	if err != nil {
		return "", err
	}

	nameGlob := ""
	if raw, ok := args["name_glob"].(string); ok {
		nameGlob = strings.TrimSpace(raw)
	}
	offset := toolkit.ClampIntArg(args, "offset", 0, 0, 1_000_000)
	depthArg := findDepthArg(args, nameGlob)
	resultsArg := toolkit.BoundedIntArg(args, "max_results", safecmd.FindMaxResults, 1, safecmd.FindMaxResults)
	maxDepth := depthArg.Effective
	maxResults := resultsArg.Effective
	entryType, err := parseFindEntryType(args)
	if err != nil {
		return "", err
	}

	resp := findResponse{Results: []findResult{}, Offset: offset}
	skipped := 0
	matchedTotal := 0
	truncated := false
	deeperPathsOmitted := false

	union := projectroot.IsUnionDiscoveryPath(relRoot)
	if union {
		for _, root := range roots {
			if err := t.runFindRoot(ctx, tctx, root, root.Path, projectpaths.QualifyAbs(tctx, root, root.Path),
				maxDepth, maxResults, offset, nameGlob, entryType, nil, &resp,
				&skipped, &matchedTotal, &truncated, &deeperPathsOmitted); err != nil {
				return "", err
			}
		}
	} else {
		resolved, err := safecmd.ResolvePath(ctx, t.Boundary, tctx, relRoot)
		if err != nil {
			return "", err
		}
		if err := t.runFindRoot(ctx, tctx, resolved.Root, resolved.Abs, resolved.DisplayPath,
			maxDepth, maxResults, offset, nameGlob, entryType, nil, &resp,
			&skipped, &matchedTotal, &truncated, &deeperPathsOmitted); err != nil {
			return "", err
		}
	}

	resp.Truncated = truncated
	resp.TotalResults = matchedTotal
	resp.MaxDepth = maxDepth
	resp.MaxResults = maxResults
	resp.DeeperPathsOmitted = deeperPathsOmitted
	if resp.DeeperPathsOmitted && len(resp.Results) == 0 {
		resp.DeeperPathsOmitted = false
	}
	if resp.DeeperPathsOmitted {
		resp.DepthNotice = fmt.Sprintf(
			"Walk did not descend past max_depth=%d; deeper matching entries may exist — narrow path or raise max_depth",
			resp.MaxDepth,
		)
	}
	if truncated {
		next := offset + len(resp.Results)
		resp.NextOffset = &next
	}
	if banner := findTruncationBanner(resp, depthArg, resultsArg); banner != "" {
		resp.TruncationBanner = banner
	}
	if findIsUnbounded(args, truncated) {
		collected := append([]findResult(nil), resp.Results...)
		resp = buildFindZoomedOutResponse(ctx, resp, collected)
	}
	resp.Inventory = inventoryFactsFrom(ctx)
	for _, result := range resp.Results {
		tctx.RecordSourcePath(result.Path, navigationEntryKind(result.Type))
	}
	for _, hit := range resp.Highlights {
		tctx.RecordSourcePath(hit.Path, api.NavigationEntryKindFile)
	}
	return t.encodeFindResponse(relRoot, resp)
}

// findDepthUnbounded walks every depth below the find root.
const findDepthUnbounded = 0

// findDepthArg shapes listings by depth. A name_glob search walks every depth
// unless the caller limits it, so a match is never hidden by where it lives.
func findDepthArg(args map[string]any, nameGlob string) toolkit.BoundedInt {
	fallback := safecmd.FindListingDepth
	if nameGlob != "" {
		fallback = findDepthUnbounded
	}
	return toolkit.BoundedIntArg(args, "max_depth", fallback, 1, math.MaxInt)
}

func (t *FindTool) runFindRoot(
	ctx context.Context,
	tctx tools.ToolContext,
	root projectroot.RootRef,
	fullRoot, displayRoot string,
	maxDepth, maxResults, offset int,
	nameGlob string,
	entryType findEntryType,
	onMatch func(findResult),
	resp *findResponse,
	skipped, matchedTotal *int,
	truncated, deeperPathsOmitted *bool,
) error {
	nameFilter, err := compileSurveyGlob("name_glob", nameGlob)
	if err != nil {
		return err
	}
	info, err := os.Stat(fullRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return safecmd.Reject("FIND_PATH_NOT_FOUND", map[string]any{"path": displayRoot})
		}
		return fmt.Errorf("find failed: %w", err)
	}

	if !info.IsDir() {
		scopeRel := projectroot.ScopeRel(root, fullRoot)
		if t.matchesEntry(scopeRel, nameFilter, entryType, info) {
			*matchedTotal++
			result := makeFindResult(displayRoot, info)
			if onMatch != nil {
				onMatch(result)
				return nil
			}
			if *skipped < offset {
				*skipped++
				return nil
			}
			if len(resp.Results) < maxResults {
				resp.Results = append(resp.Results, result)
			} else {
				*truncated = true
			}
		}
		return nil
	}
	readFilter, err := t.Boundary.CompileReadFilter(ctx, root.Path, tctx.ProfileID())
	if err != nil {
		return err
	}

	return t.walkFindTree(findWalkParams{
		ctx: ctx, tctx: tctx, root: root, fullRoot: fullRoot,
		maxDepth: maxDepth, maxResults: maxResults, offset: offset,
		nameFilter: nameFilter, entryType: entryType,
		resp:    resp,
		skipped: skipped, matchedTotal: matchedTotal, truncated: truncated, deeperPathsOmitted: deeperPathsOmitted,
		catalog: t.Catalog, readFilter: readFilter, onMatch: onMatch,
	})
}

func (t *FindTool) matchesEntry(relSlash string, nameFilter sandbox.EntryGlob, entryType findEntryType, info fs.FileInfo) bool {
	if !nameFilter.Match(relSlash) {
		return false
	}
	switch entryType {
	case findTypeFile:
		return !info.IsDir()
	case findTypeDir:
		return info.IsDir()
	default:
		return true
	}
}

func (t *FindTool) encodeFindResponse(root string, resp findResponse) (string, error) {
	pathsTouched := len(resp.Results)
	if pathsTouched == 0 && len(resp.Highlights) > 0 {
		pathsTouched = len(resp.Highlights)
	}
	return safecmd.Shape(safecmd.ShapeInput{
		Tool:         "find",
		Path:         root,
		PathsTouched: pathsTouched,
		Truncated:    resp.Truncated,
		Banner:       resp.TruncationBanner,
		Selected:     resp.Selected,
		Total:        resp.Total,
		Value:        resp,
	})
}

func findTruncationBanner(resp findResponse, depthArg, resultsArg toolkit.BoundedInt) string {
	var parts []string
	parts = toolkit.AppendClampBanner(parts, "max_depth", depthArg)
	parts = toolkit.AppendClampBanner(parts, "max_results", resultsArg)
	if resp.Truncated && resp.NextOffset != nil {
		parts = append(parts, fmt.Sprintf("showing %d of %d entries from offset %d; use offset=%d",
			len(resp.Results), resp.TotalResults, resp.Offset, *resp.NextOffset))
	}
	if len(parts) == 0 {
		return ""
	}
	return toolkit.TruncationBanner(strings.Join(parts, "; "))
}

func makeFindResult(relSlash string, info fs.FileInfo) findResult {
	entry := findResult{
		Path: relSlash,
		Type: "file",
	}
	if info.IsDir() {
		entry.Type = "dir"
		return entry
	}
	size := info.Size()
	entry.Size = &size
	return entry
}
