package survey

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func listDirSingleLevel(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	root projectroot.RootRef,
	fullPath, relPath string,
	includeHidden bool,
) ([]listDirEntry, error) {
	opts := sandbox.SurveyOptions{IncludeHidden: includeHidden}
	entries, ok, err := workerBranchReadDir(ctx, tctx, fullPath, opts)
	if err != nil {
		return nil, err
	}
	if !ok {
		entries, err = sandbox.SurveyReadDir(fullPath, relPath, opts)
		if err != nil {
			return nil, err
		}
	}
	all := make([]listDirEntry, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.DirEntry.Info()
		if infoErr != nil {
			continue
		}
		all = append(all, makeListDirEntry(ctx, boundary, tctx, root,
			entry.DirEntry.Name(), entry.Rel, entry.Abs, info))
	}
	return all, nil
}

func listDirWalk(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	root projectroot.RootRef,
	fullRoot, relRoot string,
	maxDepth int,
	includeHidden bool,
) ([]listDirEntry, error) {
	var all []listDirEntry
	admit := func(_, abs string, _ bool) bool {
		return boundary == nil || boundary.AssertReadScope(
			ctx, root.Path, projectroot.ScopeRel(root, abs), tctx.ProfileID(),
		) == nil
	}
	opts := sandbox.SurveyOptions{
		IncludeHidden: includeHidden,
		MaxDepth:      maxDepth,
		Admit:         admit,
	}
	visit := func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
		info, infoErr := entry.DirEntry.Info()
		if infoErr != nil {
			return sandbox.SurveyContinue, nil
		}
		displayPath := projectpaths.QualifyAbs(tctx, root, entry.Abs)
		relFromRoot, relErr := filepath.Rel(filepath.ToSlash(relRoot), displayPath)
		if relErr != nil || relFromRoot == "." {
			return sandbox.SurveyContinue, nil
		}
		all = append(all, makeListDirEntry(ctx, boundary, tctx, root,
			filepath.ToSlash(relFromRoot), displayPath, entry.Abs, info))
		return sandbox.SurveyContinue, nil
	}
	if ok, err := workerBranchSurveyWalk(ctx, tctx, fullRoot, opts, visit); ok {
		return all, err
	}
	err := sandbox.SurveyWalk(ctx, fullRoot, opts, visit)
	if err != nil {
		return nil, err
	}
	return all, nil
}

func makeListDirEntry(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	root projectroot.RootRef,
	name, relSlash, absPath string,
	info fs.FileInfo,
) listDirEntry {
	item := listDirEntry{
		Name:      name,
		Path:      relSlash,
		Type:      "file",
		Mode:      sourceview.FormatFileMode(uint32(info.Mode().Perm())),
		Modified:  info.ModTime().UTC().Format(time.RFC3339),
		IsSymlink: info.Mode()&os.ModeSymlink != 0,
	}
	if info.IsDir() {
		item.Type = "dir"
	} else {
		size := info.Size()
		item.Size = &size
	}
	if item.IsSymlink {
		attachListDirSymlinkTarget(ctx, boundary, tctx, root, absPath, &item)
	}
	return item
}

func attachListDirSymlinkTarget(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	root projectroot.RootRef,
	absPath string,
	entry *listDirEntry,
) {
	if boundary == nil {
		return
	}
	target, err := os.Readlink(absPath)
	if err != nil {
		return
	}
	scopeRel := projectroot.ScopeRel(root, absPath)
	resolvedRel := sourceview.SymlinkTarget(root.Path, scopeRel, target)
	if resolvedRel == "" {
		return
	}
	if err := boundary.AssertReadScope(ctx, root.Path, resolvedRel, tctx.ProfileID()); err != nil {
		return
	}
	// Join without EvalSymlinks so QualifyAbs stays under root.Path (macOS /var vs /private/var).
	targetAbs := filepath.Join(root.Path, filepath.FromSlash(resolvedRel))
	qualified := projectpaths.QualifyAbs(tctx, root, targetAbs)
	entry.SymlinkTarget = &qualified
}

func (t *ListDirTool) listRootCatalog(
	ctx context.Context,
	tctx tools.ToolContext,
	root projectroot.RootRef,
	fullPath string,
	maxDepth int,
	includeHidden bool,
) ([]listDirEntry, bool) {
	if catalogOrProcess(t.Catalog).BoundaryPath(ctx, root.Path, projectroot.ScopeRel(root, fullPath), true) != "" {
		return nil, false
	}
	current := catalogOrProcess(t.Catalog).Current(ctx, tctx.Identity.ProjectID, []sourcecatalog.Root{{ID: root.ID, Path: root.Path}})
	if current.State != sourcecatalog.StateReady {
		return nil, false
	}
	scopeRel := projectroot.ScopeRel(root, fullPath)
	if maxDepth == 1 {
		children, ok := current.Listing(root.ID, scopeRel)
		if !ok {
			return nil, false
		}
		out := make([]listDirEntry, 0, len(children))
		for _, child := range children {
			if !includeHidden && strings.HasPrefix(child.Name, ".") {
				continue
			}
			if t.Boundary != nil && t.Boundary.AssertReadScope(ctx, root.Path, child.Path, tctx.ProfileID()) != nil {
				continue
			}
			absPath := filepath.Join(root.Path, filepath.FromSlash(child.Path))
			displayPath := projectpaths.QualifyAbs(tctx, root, absPath)
			out = append(out, makeListDirCatalogEntry(ctx, t.Boundary, tctx, root, child.Name, displayPath, absPath, child))
		}
		return out, true
	}
	return nil, false
}

func makeListDirCatalogEntry(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	root projectroot.RootRef,
	name, relSlash, absPath string,
	entry sourcecatalog.Entry,
) listDirEntry {
	item := listDirEntry{
		Name:      name,
		Path:      relSlash,
		Type:      "file",
		Mode:      sourceview.FormatFileMode(entry.Mode),
		Modified:  entry.Modified.UTC().Format(time.RFC3339),
		IsSymlink: entry.IsSymlink,
	}
	if entry.IsDir {
		item.Type = "dir"
	} else {
		size := entry.Size
		item.Size = &size
	}
	if item.IsSymlink {
		attachListDirSymlinkTarget(ctx, boundary, tctx, root, absPath, &item)
	}
	return item
}
