package survey

import (
	"context"
	"fmt"
	"io/fs"
	"os"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

// FindTool discovers paths with bounded depth and paginated results.
type FindTool struct {
	Boundary *sandbox.Boundary
	Catalog  *sourcecatalog.Catalog
}

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
	// Structured identity of the entry under its project root; never serialized.
	rootID   string `json:"-"`
	scopeRel string `json:"-"`
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
	Note               string          `json:"note,omitempty"`
	Highlights         []readHighlight `json:"highlights,omitempty"`
	Gloss              []readGlossLine `json:"gloss,omitempty"`
	Selected           int             `json:"selected,omitempty"`
	Total              int             `json:"total,omitempty"`
	TruncationBanner   string          `json:"truncation_banner,omitempty"`
}

// findDepthUnbounded walks every depth below the find root.
const findDepthUnbounded = 0

func (t *FindTool) runFindRoot(
	ctx context.Context,
	tctx tools.ToolContext,
	root projectroot.RootRef,
	fullRoot, displayRoot string,
	maxDepth, maxResults, offset int,
	nameGlob string,
	entryType findEntryType,
	includeIgnored bool,
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
			result := makeFindResult(root.ID, scopeRel, displayRoot, info)
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

	var ignore sandbox.SurveyScope
	if !includeIgnored {
		ignore = newIgnoreScope(root, fullRoot)
	}
	return t.walkFindTree(findWalkParams{
		ctx: ctx, tctx: tctx, root: root, fullRoot: fullRoot,
		maxDepth: maxDepth, maxResults: maxResults, offset: offset,
		nameFilter: nameFilter, entryType: entryType,
		resp:    resp,
		skipped: skipped, matchedTotal: matchedTotal, truncated: truncated, deeperPathsOmitted: deeperPathsOmitted,
		catalog: t.Catalog, readFilter: readFilter, ignore: ignore, onMatch: onMatch,
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

func makeFindResult(rootID, scopeRel, relSlash string, info fs.FileInfo) findResult {
	entry := findResult{
		Path:     relSlash,
		Type:     "file",
		rootID:   rootID,
		scopeRel: scopeRel,
	}
	if info.IsDir() {
		entry.Type = "dir"
		return entry
	}
	size := info.Size()
	entry.Size = &size
	return entry
}
