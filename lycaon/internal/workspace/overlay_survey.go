package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// OverlaySurveyReadDir lists one branch directory.
func OverlaySurveyReadDir(branchRoot, rel string, opts sandbox.SurveyOptions) ([]sandbox.SurveyEntry, error) {
	branchRoot = strings.TrimSpace(branchRoot)
	if branchRoot == "" || !filepath.IsAbs(branchRoot) {
		return nil, fmt.Errorf("absolute branch root required")
	}
	branchRoot = filepath.Clean(branchRoot)
	rel = strings.TrimSpace(rel)
	if filepath.IsAbs(filepath.FromSlash(rel)) || sandbox.HasParentTraversal(rel) {
		return nil, fmt.Errorf("invalid branch path %q", rel)
	}
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	branchDir := branchRoot
	if rel != "" && rel != "." {
		branchDir = filepath.Join(branchRoot, filepath.FromSlash(rel))
	}
	entries, err := os.ReadDir(branchDir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	out := make([]sandbox.SurveyEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !opts.IncludeHidden && sandbox.IsHiddenName(name) {
			continue
		}
		childRel := name
		if rel != "" && rel != "." {
			childRel = rel + "/" + name
		}
		if e.IsDir() && surveySkipsOverlayDir(name, opts) {
			continue
		}
		abs := filepath.Join(branchDir, name)
		out = append(out, sandbox.SurveyEntry{
			Rel:       childRel,
			Abs:       abs,
			Depth:     strings.Count(childRel, "/") + 1,
			IsDir:     e.IsDir(),
			IsSymlink: e.Type()&fs.ModeSymlink != 0,
			DirEntry:  e,
		})
	}
	return out, nil
}

// OverlaySurveyWalk walks a branch.
func OverlaySurveyWalk(
	ctx context.Context,
	branchRoot string,
	opts sandbox.SurveyOptions,
	fn func(sandbox.SurveyEntry) (sandbox.SurveyAction, error),
) error {
	branchRoot = filepath.Clean(strings.TrimSpace(branchRoot))
	var walk func(rel string) error
	walk = func(rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := OverlaySurveyReadDir(branchRoot, rel, opts)
		if err != nil {
			if os.IsNotExist(err) && rel != "." {
				return nil
			}
			return err
		}
		for _, entry := range entries {
			if opts.Admit != nil && !opts.Admit(entry.Rel, entry.Abs, entry.IsDir) {
				continue
			}
			if opts.MaxDepth > 0 && entry.Depth > opts.MaxDepth {
				continue
			}
			action, err := fn(entry)
			if err != nil {
				return err
			}
			switch action {
			case sandbox.SurveyStop:
				return nil
			case sandbox.SurveySkipDir:
				continue
			case sandbox.SurveyContinue:
			}
			if entry.IsDir && !entry.IsSymlink {
				if opts.MaxDepth > 0 && entry.Depth >= opts.MaxDepth {
					continue
				}
				if err := walk(entry.Rel); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(".")
}

func surveySkipsOverlayDir(name string, opts sandbox.SurveyOptions) bool {
	if !opts.IncludeVCSMetadata && sandbox.IsVCSDirBaseName(name) {
		return true
	}
	if !opts.IncludeEngineOverlay && sandbox.IsEngineOverlayBaseName(name) {
		return true
	}
	return false
}
