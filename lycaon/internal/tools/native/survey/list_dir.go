package survey

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/paginate"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

// ListDirTool lists scoped entries or a bounded repository map.
type ListDirTool struct {
	ObserveMap func(mapWork)
	Boundary   *sandbox.Boundary
	Catalog    *sourcecatalog.Catalog
	UnionBrief repomap.UnionOrientationBrief
}

func (t *ListDirTool) Name() string { return "list_dir" }

type listDirEntry struct {
	Name          string  `json:"name"`
	Path          string  `json:"path,omitempty"`
	Type          string  `json:"type"`
	Size          *int64  `json:"size,omitempty"`
	Mode          string  `json:"mode"`
	Modified      string  `json:"modified"`
	IsSymlink     bool    `json:"is_symlink"`
	SymlinkTarget *string `json:"symlink_target,omitempty"`
}

type listDirResponse struct {
	Coverage         *mapCoverage           `json:"coverage,omitempty"`
	NextActions      []summarize.NextAction `json:"next_actions,omitempty"`
	Path             string                 `json:"path"`
	Entries          []listDirEntry         `json:"entries,omitempty"`
	Offset           int                    `json:"offset,omitempty"`
	TotalEntries     int                    `json:"total_entries,omitempty"`
	Truncated        bool                   `json:"truncated"`
	NextOffset       *int                   `json:"next_offset,omitempty"`
	TruncationBanner string                 `json:"truncation_banner,omitempty"`

	View             string               `json:"view,omitempty"`
	Tree             *directoryMapNode    `json:"tree,omitempty"`
	Diagnostics      *repomap.Diagnostics `json:"diagnostics,omitempty"`
	OrientationBrief string               `json:"orientation_brief,omitempty"`
	Note             string               `json:"note,omitempty"`
}

func (t *ListDirTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	relPath, _ := args["path"].(string)
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", toolkit.MissingArg("path")
	}
	if err := safecmd.RejectPathEscape(relPath); err != nil {
		return "", err
	}
	if listDirHasExpressedScope(args, relPath) {
		if cursor, _ := args["cursor"].(string); cursor != "" {
			return "", mapCursorReject()
		}
		return t.runLiteralListing(ctx, args, tctx, relPath)
	}
	cursor, _ := args["cursor"].(string)
	return t.runZoomedMap(ctx, tctx, relPath, cursor)
}

func (t *ListDirTool) runZoomedMap(ctx context.Context, tctx tools.ToolContext, relPath, cursor string) (string, error) {
	displayPath := filepath.ToSlash(relPath)
	subpath := displayPath
	if subpath == "." {
		subpath = ""
	}
	resp, err := buildListDirZoomedMapResponse(ctx, t, tctx, displayPath, subpath, cursor)
	if err != nil {
		return "", err
	}
	recordListedSources(tctx, resp)
	return safecmd.Shape(safecmd.ShapeInput{
		Tool:         "list_dir",
		Path:         resp.Path,
		PathsTouched: 1,
		Truncated:    resp.Truncated,
		Value:        resp,
	})
}

func (t *ListDirTool) runLiteralListing(ctx context.Context, args map[string]any, tctx tools.ToolContext, relPath string) (string, error) {
	includeHidden := toolkit.BoolArg(args, "include_hidden", false)
	maxDepth := toolkit.ClampIntArg(args, "max_depth", 1, 1, safecmd.ListDirMaxDepth)
	offset := toolkit.ClampIntArg(args, "offset", 0, 0, 1_000_000)
	entriesArg := toolkit.BoundedIntArg(args, "max_entries", safecmd.ListDirMaxEntries, 1, safecmd.ListDirMaxEntries)
	maxEntries := entriesArg.Effective

	var all []listDirEntry
	if projectroot.IsUnionDiscoveryPath(relPath) {
		roots, err := projectpaths.UnionDiscoveryRoots(ctx, tctx, relPath)
		if err != nil {
			return "", err
		}
		for _, root := range roots {
			displayRoot := projectpaths.QualifyAbs(tctx, root, root.Path)
			entries, err := t.listRoot(ctx, tctx, root, root.Path, displayRoot, maxDepth, includeHidden)
			if err != nil {
				return "", fmt.Errorf("list_dir failed: %w", err)
			}
			all = append(all, entries...)
		}
	} else {
		resolved, err := safecmd.ResolvePath(ctx, t.Boundary, tctx, relPath)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(resolved.Abs)
		if err != nil {
			if os.IsNotExist(err) {
				return "", safecmd.Reject("LIST_DIR_PATH_NOT_FOUND", map[string]any{"path": resolved.DisplayPath})
			}
			return "", fmt.Errorf("list_dir failed: %w", err)
		}
		if !info.IsDir() {
			return "", safecmd.Reject("LIST_DIR_NOT_DIRECTORY", map[string]any{"path": resolved.DisplayPath})
		}
		all, err = t.listRoot(ctx, tctx, resolved.Root, resolved.Abs, resolved.DisplayPath, maxDepth, includeHidden)
		if err != nil {
			return "", fmt.Errorf("list_dir failed: %w", err)
		}
		relPath = resolved.DisplayPath
	}

	page, total, truncated, nextOffset := paginate.Slice(all, offset, maxEntries)
	resp := listDirResponse{
		Path:         filepath.ToSlash(relPath),
		Entries:      page,
		Offset:       offset,
		TotalEntries: total,
		Truncated:    truncated,
		NextOffset:   nextOffset,
	}
	if total == 0 {
		resp.Note = "Empty — 0 entries at this level, hidden included."
		if !includeHidden {
			resp.Note = "Empty — 0 visible entries at this level. Hidden entries are excluded; include_hidden=true would include them."
		}
	}
	var bannerParts []string
	bannerParts = toolkit.AppendClampBanner(bannerParts, "max_entries", entriesArg)
	if truncated && nextOffset != nil {
		bannerParts = append(bannerParts,
			fmt.Sprintf("showing %d of %d entries from offset %d; use offset=%d", len(page), total, offset, *nextOffset),
		)
	}
	if len(bannerParts) > 0 {
		resp.TruncationBanner = toolkit.TruncationBanner(strings.Join(bannerParts, "; "))
	}

	recordListedSources(tctx, resp)
	return safecmd.Shape(safecmd.ShapeInput{
		Tool:         "list_dir",
		Path:         resp.Path,
		PathsTouched: len(resp.Entries),
		Truncated:    resp.Truncated,
		Banner:       resp.TruncationBanner,
		Value:        resp,
	})
}

func (t *ListDirTool) listRoot(
	ctx context.Context,
	tctx tools.ToolContext,
	root projectroot.RootRef,
	fullPath, relPath string,
	maxDepth int,
	includeHidden bool,
) ([]listDirEntry, error) {
	if strings.TrimSpace(tctx.WorkerBranchRoot) == "" && t.Catalog != nil {
		if entries, ok := t.listRootCatalog(ctx, tctx, root, fullPath, maxDepth, includeHidden); ok {
			return entries, nil
		}
	}
	if maxDepth == 1 {
		return listDirSingleLevel(ctx, t.Boundary, tctx, root, fullPath, filepath.ToSlash(relPath), includeHidden)
	}
	return listDirWalk(ctx, t.Boundary, tctx, root, fullPath, filepath.ToSlash(relPath), maxDepth, includeHidden)
}
