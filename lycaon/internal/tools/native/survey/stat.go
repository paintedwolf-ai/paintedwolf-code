package survey

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/pkg/api"
)

// StatTool returns file metadata for repo-relative paths.
type StatTool struct {
	Boundary *sandbox.Boundary
	Catalog  *sourcecatalog.Catalog
}

func (t *StatTool) Name() string { return "stat" }

type statResult struct {
	Path          string  `json:"path"`
	Mode          string  `json:"mode"`
	Size          int64   `json:"size"`
	Modified      string  `json:"modified"`
	IsDir         bool    `json:"is_dir"`
	IsSymlink     bool    `json:"is_symlink"`
	SymlinkTarget *string `json:"symlink_target,omitempty"`
	EntryCount    *int    `json:"entry_count,omitempty"`
}

type statResponse struct {
	Results []statResult `json:"results"`
}

func (t *StatTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	paths, err := toolkit.ParseStringSliceArg(args, "paths", safecmd.StatCaps().ResultCount)
	if err != nil {
		return "", err
	}
	resp := statResponse{Results: []statResult{}}
	for _, relPath := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		resolved, err := safecmd.ResolvePath(ctx, t.Boundary, tctx, relPath)
		if err != nil {
			return "", err
		}
		fullPath := resolved.Abs
		relSlash := filepath.ToSlash(resolved.DisplayPath)
		info, err := os.Lstat(fullPath)
		if err != nil {
			return "", fmt.Errorf("stat %s: %w", relPath, err)
		}
		entry := statEntryFromInfo(relSlash, info)
		if info.IsDir() {
			counted := false
			if t.Catalog != nil {
				current := catalogOrProcess(t.Catalog).Current(ctx, tctx.Identity.ProjectID, []sourcecatalog.Root{{ID: resolved.Root.ID, Path: resolved.Root.Path}})
				if current.State == sourcecatalog.StateReady {
					rel := projectroot.ScopeRel(resolved.Root, fullPath)
					if children, ok := current.Listing(resolved.Root.ID, rel); ok {
						count := 0
						for _, child := range children {
							if !strings.HasPrefix(child.Name, ".") {
								count++
							}
						}
						entry.EntryCount = &count
						counted = true
					}
				}
			}
			if !counted {
				if count, err := countDirEntries(fullPath, false, relSlash); err == nil {
					entry.EntryCount = &count
				}
			}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(fullPath); err == nil {
				if resolvedRel := sourceview.SymlinkTarget(resolved.Root.Path, resolved.ScopeRel, target); resolvedRel != "" {
					if err := t.Boundary.AssertReadScope(ctx, resolved.Root.Path, resolvedRel, tctx.ProfileID()); err == nil {
						if targetAbs, absErr := t.Boundary.ResolveAbs(resolved.Root.Path, resolvedRel); absErr == nil {
							qualified := projectpaths.QualifyAbs(tctx, resolved.Root, targetAbs)
							entry.SymlinkTarget = &qualified
						}
					}
				}
			}
		}
		resp.Results = append(resp.Results, entry)
	}
	root := "."
	if len(paths) == 1 {
		root = paths[0]
	}
	for _, result := range resp.Results {
		kind := api.NavigationEntryKindFile
		if result.IsDir {
			kind = api.NavigationEntryKindFolder
		}
		tctx.RecordSourcePath(result.Path, kind)
	}
	return safecmd.Shape(safecmd.ShapeInput{
		Tool:         "stat",
		Path:         root,
		PathsTouched: len(resp.Results),
		Value:        resp,
	})
}

func statEntryFromInfo(relSlash string, info os.FileInfo) statResult {
	return statResult{
		Path:      relSlash,
		Mode:      sourceview.FormatFileMode(uint32(info.Mode().Perm())),
		Size:      info.Size(),
		Modified:  info.ModTime().UTC().Format(time.RFC3339),
		IsDir:     info.IsDir(),
		IsSymlink: info.Mode()&os.ModeSymlink != 0,
	}
}

func countDirEntries(fullPath string, includeHidden bool, relSlash string) (int, error) {
	entries, err := sandbox.SurveyReadDir(fullPath, relSlash, sandbox.SurveyOptions{IncludeHidden: includeHidden})
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}
