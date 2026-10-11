package survey

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/toolscope"
)

// GrepScopeRequiredCode is the structured reject when open-root grep repeats after denseness-first.
const GrepScopeRequiredCode = "GREP_SCOPE_REQUIRED"

// maybeGuardOpenRootScope handles open-root denseness gating.
func (t *GrepTool) maybeGuardOpenRootScope(
	ctx context.Context,
	args map[string]any,
	tctx tools.ToolContext,
	opts grepOptions,
) (out string, guarded bool, err error) {
	path := "."
	if raw, ok := args["path"].(string); ok && strings.TrimSpace(raw) != "" {
		path = strings.TrimSpace(raw)
	}
	if !grepOpenRootUnscoped(path, opts.pathGlob) {
		return "", false, nil
	}
	cfg := toolscope.Global()
	if t != nil && t.Scope != nil {
		cfg = *t.Scope
	}
	var fileCountSource func(string) (int, bool)
	if t != nil {
		fileCountSource = t.FileCount
	}
	fileCount, applyGuard, topLevel := resolveGrepGuardCount(tctx, fileCountSource, cfg.RootStructuralFileCount)
	if !applyGuard {
		return "", false, nil
	}
	gate := &grepDensenessGate{}
	if t != nil {
		gate = &t.densenessGate
	}
	if gate.alreadyDenseness(tctx) {
		return "", true, grepScopeRequiredReject(path, opts.pathGlob, fileCount)
	}
	gate.markDenseness(tctx)
	resp := buildGrepScopeDensenessResponse(ctx, path, fileCount, topLevel)
	shaped, err := safecmd.Shape(safecmd.ShapeInput{
		Tool:         "grep",
		Path:         path,
		PathsTouched: 0,
		Truncated:    true,
		Selected:     resp.Selected,
		Total:        resp.Total,
		Value:        resp,
	})
	return shaped, true, err
}

// pathGlobIsOpen reports whether path_glob is missing or an unbounded root glob.
func pathGlobIsOpen(glob string) bool {
	g := strings.TrimSpace(glob)
	switch g {
	case "", "*", "**", "**/*", "*.*":
		return true
	default:
		return false
	}
}

// grepPathIsOpenRoot reports workspace-root (or empty / union) path scope.
func grepPathIsOpenRoot(path string) bool {
	p := strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	return projectroot.IsUnionDiscoveryPath(p)
}

// grepOpenRootUnscoped is true when path is root and path_glob does not narrow.
func grepOpenRootUnscoped(path, pathGlob string) bool {
	return grepPathIsOpenRoot(path) && pathGlobIsOpen(pathGlob)
}

// resolveGrepGuardCount resolves the root file-count fact.
func resolveGrepGuardCount(
	tctx tools.ToolContext,
	fileCountSource func(projectDir string) (int, bool),
	threshold int,
) (fileCount int, applyGuard bool, topLevel []string) {
	topLevel = append([]string(nil), tctx.Source.RepoTopLevel...)
	known := tctx.Source.RepoFileCountKnown
	if tctx.Source.RepoFileCountKnown {
		fileCount = tctx.Source.RepoFileCount
	}
	root := tctx.ActiveRootPath()
	if fileCountSource != nil && root != "" {
		if n, ok := fileCountSource(root); ok {
			if !known || n > fileCount {
				fileCount = n
			}
			known = true
		}
	}
	if known {
		return fileCount, toolscope.NeedsRootScopeGuard(fileCount, true, threshold), topLevel
	}
	// Unknown counts use the denseness view.
	return 0, true, topLevel
}

// grepDensenessGate tracks open-root repeats per session.
type grepDensenessGate struct {
	// Eviction may repeat the denseness view.
	seen scopedstore.LRU[struct{}]
}

func (g *grepDensenessGate) key(tctx tools.ToolContext) string {
	sid := strings.TrimSpace(tctx.Identity.SessionID)
	if sid == "" {
		sid = "_"
	}
	pid := strings.TrimSpace(tctx.Identity.ProjectID)
	return sid + "|" + pid
}

func (g *grepDensenessGate) alreadyDenseness(tctx tools.ToolContext) bool {
	if g == nil {
		return false
	}
	_, ok := g.seen.Load(g.key(tctx))
	return ok
}

func (g *grepDensenessGate) markDenseness(tctx tools.ToolContext) {
	if g == nil {
		return
	}
	g.seen.Store(g.key(tctx), struct{}{})
}

func grepScopeRequiredReject(path, pathGlob string, fileCount int) error {
	return safecmd.Reject(GrepScopeRequiredCode, map[string]any{
		"path":       path,
		"path_glob":  pathGlob,
		"file_count": fileCount,
		"tool":       "grep",
	})
}

func buildGrepScopeDensenessResponse(ctx context.Context, path string, fileCount int, topLevel []string) grepResponse {
	dist := make([]grepDirCount, 0, len(topLevel))
	for _, dir := range topLevel {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		dist = append(dist, grepDirCount{Dir: dir, Count: 0})
	}
	vars := map[string]any{}
	if fileCount > 0 {
		vars["file_count"] = fileCount
	}
	note, err := guidance.RenderCatalog(ctx, guidance.SurveyGrepScopeDenseRef, vars)
	if err != nil {
		note = ""
	}
	return grepResponse{
		View:         surveyViewDigest,
		Matches:      nil,
		Offset:       0,
		Truncated:    true,
		Distribution: dist,
		Selected:     0,
		Total:        0,
		Note:         toolkit.AppendNote("", note+"; "+grepSpecificsAffordance(ctx)),
	}
}
