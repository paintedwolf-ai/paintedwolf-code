package survey

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

const mapPageEntries = 16
const mapReadEntries = 64
const mapTokenBudget = 1200

type directoryMapNode struct {
	Path              string              `json:"path"`
	Type              string              `json:"type"`
	ImmediateChildren *int                `json:"immediate_children,omitempty"`
	DescendantFiles   *int                `json:"descendant_files,omitempty"`
	Children          []*directoryMapNode `json:"children,omitempty"`
	ZoomIn            bool                `json:"zoom_in,omitempty"`
}

type mapCoverage struct {
	CatalogState    string `json:"catalog_state"`
	EntriesReturned int    `json:"entries_returned"`
	EntriesTotal    *int   `json:"entries_total,omitempty"`
}

type mapWork struct {
	MetadataRows, DirectoryEntries, DirectoriesOpened int
	DurationMs                                        int64
}

type directoryMapCursor struct {
	Scope    string `json:"s"`
	Revision uint64 `json:"r"`
	Page     string `json:"p"`
}

var directoryMapCursors = pagecursor.For[directoryMapCursor]("directory_map")

func buildListDirZoomedMapResponse(ctx context.Context, t *ListDirTool, tctx tools.ToolContext, display, subpath, cursor string) (listDirResponse, error) {
	started := time.Now()
	resolved, err := projectpaths.ResolveRead(ctx, t.Boundary, tctx, display)
	if err != nil {
		return listDirResponse{}, err
	}
	scope, err := t.Boundary.CompileReadScope(ctx, resolved.Root.Path, tctx.ProfileID())
	if err != nil {
		return listDirResponse{}, err
	}
	reader, status, err := catalogOrProcess(t.Catalog).Trees.OpenSummary(ctx, tctx.Identity.ProjectID,
		sourcecatalog.Root{ID: resolved.Root.ID, Path: resolved.Root.Path},
		sourcecatalog.TreeScope{Key: scope.Key, Filter: scope.Filter, PruneNestedVCS: true}, 100*time.Millisecond)
	if err != nil {
		return listDirResponse{}, err
	}
	defer func() {
		if reader != nil {
			_ = reader.Close()
		}
	}()
	resp := listDirResponse{Path: display, View: "map", Tree: &directoryMapNode{Path: display, Type: "dir"},
		Coverage: &mapCoverage{CatalogState: string(status.State)}}
	if t.UnionBrief != nil {
		resp.OrientationBrief, err = t.UnionBrief(ctx, tctx, subpath)
		if len(resp.OrientationBrief) > 1024 {
			resp.OrientationBrief = string(trimSummaryUTF8Tail([]byte(resp.OrientationBrief)[:1024]))
		}
		if err != nil {
			return resp, err
		}
	}
	work := mapWork{}
	defer func() {
		work.DurationMs = time.Since(started).Milliseconds()
		observability.LogSummarizeCall(observability.SummarizeDebugCapture{Tool: "list_dir", Work: work})
		if t.ObserveMap != nil {
			t.ObserveMap(work)
		}
	}()
	if reader == nil {
		if cursor != "" {
			return resp, mapCursorReject()
		}
		if status.State == sourcecatalog.StateFailed {
			return resp, fmt.Errorf("source catalog: %s", status.Error)
		}
		err = coldDirectoryMap(ctx, t, tctx, resolved, &resp, &work)
	} else {
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(resolved.Root.ID+"\x00"+resolved.Root.Path+"\x00"+scope.Key+"\x00"+display)))
		err = indexedDirectoryMap(ctx, reader, key, cursor, &resp)
		work.MetadataRows = reader.RowsRead
	}
	if err != nil {
		return resp, err
	}
	resp.Coverage.EntriesReturned = len(resp.Tree.Children)
	if len(resp.Tree.Children) == 0 && !resp.Truncated {
		resp.Diagnostics = &repomap.Diagnostics{HintCode: "REPO_MAP_NO_FILES", Hint: "No files in this scope."}
	}
	return resp, nil
}

func indexedDirectoryMap(ctx context.Context, reader *sourcecatalog.SummaryReader, scope, cursor string, resp *listDirResponse) error {
	position := directoryMapCursor{Scope: scope, Revision: reader.Status.Revision}
	if cursor != "" {
		pos, err := directoryMapCursors.Decode(cursor, scope)
		if err != nil || pos.Revision != reader.Status.Revision {
			return mapCursorReject()
		}
		position = pos
	}
	node, err := reader.Node(ctx, ".")
	if err != nil {
		return err
	}
	resp.Coverage.EntriesTotal = &node.Children
	resp.Tree.DescendantFiles = &node.Files
	// One-node pages let the byte budget stop at an exact continuation.
	for range mapPageEntries {
		page, pageErr := reader.Page(ctx, node, position.Page, "", nil, 1)
		if pageErr != nil {
			if errors.Is(pageErr, sourcecatalog.ErrTreeCursor) {
				return mapCursorReject()
			}
			return pageErr
		}
		if len(page.Nodes) == 0 {
			break
		}
		child := page.Nodes[0]
		row := &directoryMapNode{Path: child.Path, Type: "file"}
		if child.IsSymlink {
			row.Type = "symlink"
		}
		if child.IsDir {
			row.Type = "dir"
			row.ZoomIn = true
			row.ImmediateChildren = &child.Children
			row.DescendantFiles = &child.Files
		}
		resp.Tree.Children = append(resp.Tree.Children, row)
		previous := position.Page
		position.Page = page.Next
		if err := setMapContinuation(resp, position); err != nil {
			return err
		}
		fits, err := mapFits(*resp)
		if err != nil {
			return err
		}
		if !fits {
			resp.Tree.Children = resp.Tree.Children[:len(resp.Tree.Children)-1]
			position.Page = previous
			break
		}

		if position.Page == "" {
			return nil
		}
	}
	return setMapContinuation(resp, position)
}

func setMapContinuation(resp *listDirResponse, position directoryMapCursor) error {
	resp.NextActions = nil
	resp.Truncated = position.Page != ""
	if position.Page == "" {
		return nil
	}
	token, err := directoryMapCursors.Encode(position.Scope, position)
	if err != nil {
		return err
	}
	resp.NextActions = []summarize.NextAction{{Tool: "list_dir", Path: resp.Path, Cursor: token, Why: "More entries"}}
	return nil
}

func mapFits(resp listDirResponse) (bool, error) {
	resp.Coverage.EntriesReturned = len(resp.Tree.Children)
	raw, err := safecmd.Shape(safecmd.ShapeInput{Tool: "list_dir", Path: resp.Path, PathsTouched: 1, Truncated: resp.Truncated, Value: resp})
	return len(raw) <= mapTokenBudget*4, err
}

func coldDirectoryMap(ctx context.Context, t *ListDirTool, tctx tools.ToolContext, resolved projectpaths.Resolved, resp *listDirResponse, work *mapWork) error {
	loc, err := sourceview.ReadLocation(ctx, t.Boundary, tctx, resolved.Abs)
	if err != nil {
		return err
	}
	f, err := fseffect.OpenRead(loc)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	work.DirectoriesOpened++
	entries, err := f.ReadDir(mapReadEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	work.DirectoryEntries = len(entries)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})
	resp.Truncated = len(entries) == mapReadEntries
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.ToSlash(filepath.Join(resolved.DisplayPath, e.Name()))
		if sandbox.ShouldSkipDir(path, e.Name()) {
			continue
		}
		child, err := projectpaths.ResolveRead(ctx, t.Boundary, tctx, path)
		if err != nil {
			continue
		}
		if e.IsDir() && gitrepo.IsRoot(child.Abs) {
			continue
		}
		kind := "file"
		if e.IsDir() {
			kind = "dir"
		}
		if e.Type()&os.ModeSymlink != 0 {
			kind = "symlink"
		}
		resp.Tree.Children = append(resp.Tree.Children, &directoryMapNode{Path: path, Type: kind, ZoomIn: e.IsDir()})
		raw, marshalErr := surveyjson.Marshal(resp)
		if marshalErr != nil {
			return marshalErr
		}
		if len(raw) > (mapTokenBudget-300)*4 || len(resp.Tree.Children) > mapPageEntries {
			resp.Tree.Children = resp.Tree.Children[:len(resp.Tree.Children)-1]
			resp.Truncated = true
			break
		}
	}
	if !resp.Truncated {
		n := len(resp.Tree.Children)
		resp.Coverage.EntriesTotal = &n
	}
	return nil
}

func mapCursorReject() error { return safecmd.Reject("LIST_DIR_CURSOR_STALE", map[string]any{}) }
