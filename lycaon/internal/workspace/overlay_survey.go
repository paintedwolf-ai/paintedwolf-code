package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/sandbox"
)

type layeredSurveyDirEntry struct {
	name string
	mode fs.FileMode
	size int64
	mod  time.Time
}

func (d layeredSurveyDirEntry) Name() string               { return d.name }
func (d layeredSurveyDirEntry) IsDir() bool                { return d.mode.IsDir() }
func (d layeredSurveyDirEntry) Type() fs.FileMode          { return d.mode.Type() }
func (d layeredSurveyDirEntry) Info() (fs.FileInfo, error) { return d, nil }
func (d layeredSurveyDirEntry) Size() int64                { return d.size }
func (d layeredSurveyDirEntry) Mode() fs.FileMode          { return d.mode }
func (d layeredSurveyDirEntry) ModTime() time.Time         { return d.mod }
func (d layeredSurveyDirEntry) Sys() any                   { return nil }

// OverlayWorkspaceDivergencePaths returns workspace divergence paths without a baseline: O(changed)
// from a mounted view's consumer.Engine (#1664), and from a materialized view's stat manifest, which
// reads only files whose stat differs.
func OverlayWorkspaceDivergencePaths(ctx context.Context, branchRoot string) ([]string, bool, error) {
	eng, ok := consumer.LookupBranchEngine(branchRoot)
	if !ok || eng == nil {
		return materializedDivergencePaths(ctx, branchRoot)
	}
	paths, err := eng.DivergenceRelativePaths(ctx, "")
	return paths, true, err
}

func inventoryItemToSurveyEntry(branchRoot, childRel string, item consumer.InventoryEntry) sandbox.SurveyEntry {
	name := filepath.Base(filepath.FromSlash(childRel))
	mode := item.Mode
	if item.Kind == layeredfs.EntryDirectory {
		mode |= fs.ModeDir
	} else if item.Kind == layeredfs.EntrySymlink {
		mode |= fs.ModeSymlink
	}
	return sandbox.SurveyEntry{
		Rel: childRel, Abs: filepath.Join(branchRoot, filepath.FromSlash(childRel)),
		Depth: strings.Count(childRel, "/") + 1, IsDir: item.Kind == layeredfs.EntryDirectory,
		IsSymlink: item.Kind == layeredfs.EntrySymlink,
		DirEntry:  layeredSurveyDirEntry{name: name, mode: mode, size: item.Size, mod: item.ModTime},
	}
}

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
	if eng, rootID, scopedRel, ok := consumer.ResolveBranchEnginePath(branchRoot, rel); ok && eng != nil {
		items, err := eng.ListDirectory(context.Background(), layeredfs.QualifiedPath{RootID: rootID, Rel: scopedRel}, "")
		if err != nil {
			return nil, err
		}
		out := make([]sandbox.SurveyEntry, 0, len(items))
		for _, item := range items {
			name := filepath.Base(filepath.FromSlash(item.Path.Rel))
			childRel := name
			if rel != "" && rel != "." {
				childRel = rel + "/" + name
			}
			se := inventoryItemToSurveyEntry(branchRoot, childRel, item)
			if (!opts.IncludeHidden && sandbox.IsHiddenName(name)) || (se.IsDir && surveySkipsOverlayDir(name, opts)) {
				continue
			}
			out = append(out, se)
		}
		return out, nil
	}
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
		if (!opts.IncludeHidden && sandbox.IsHiddenName(name)) || (e.IsDir() && surveySkipsOverlayDir(name, opts)) {
			continue
		}
		childRel := name
		if rel != "" && rel != "." {
			childRel = rel + "/" + name
		}
		out = append(out, sandbox.SurveyEntry{
			Rel: childRel, Abs: filepath.Join(branchDir, name), Depth: strings.Count(childRel, "/") + 1,
			IsDir: e.IsDir(), IsSymlink: e.Type()&fs.ModeSymlink != 0, DirEntry: e,
		})
	}
	return out, nil
}

// OverlaySurveyWalk walks a branch, using the indexed consumer overlay when available (#1664).
func OverlaySurveyWalk(ctx context.Context, branchRoot string, opts sandbox.SurveyOptions, fn func(sandbox.SurveyEntry) (sandbox.SurveyAction, error)) error {
	branchRoot = filepath.Clean(strings.TrimSpace(branchRoot))
	if eng, rootID, scopedRel, ok := consumer.ResolveBranchEnginePath(branchRoot, "."); ok && eng != nil && eng.HasIndex() {
		prefix := strings.TrimPrefix(scopedRel, ".")
		res, err := eng.SurveyBranch(ctx, consumer.SurveyOptions{RootIDs: []string{rootID}, PathPrefix: prefix})
		if err != nil {
			return err
		}
		var skipped []string
		for _, item := range res.Entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			rel := item.Path.Rel
			if prefix != "" {
				rel = strings.TrimPrefix(rel, prefix+"/")
			}
			if rel == "" || rel == "." || hasAnySkippedPrefix(rel, skipped) {
				continue
			}
			entry := inventoryItemToSurveyEntry(branchRoot, rel, item)
			name := entry.DirEntry.Name()
			if (!opts.IncludeHidden && sandbox.IsHiddenName(name)) || (entry.IsDir && surveySkipsOverlayDir(name, opts)) {
				continue
			}
			if (opts.Admit != nil && !opts.Admit(entry.Rel, entry.Abs, entry.IsDir)) || (opts.MaxDepth > 0 && entry.Depth > opts.MaxDepth) {
				continue
			}
			action, err := fn(entry)
			if err != nil || action == sandbox.SurveyStop {
				return err
			}
			if action == sandbox.SurveySkipDir && entry.IsDir {
				skipped = append(skipped, rel)
			}
		}
		return nil
	}

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
			if (opts.Admit != nil && !opts.Admit(entry.Rel, entry.Abs, entry.IsDir)) || (opts.MaxDepth > 0 && entry.Depth > opts.MaxDepth) {
				continue
			}
			action, err := fn(entry)
			if err != nil || action == sandbox.SurveyStop {
				return err
			}
			if action == sandbox.SurveySkipDir {
				continue
			}
			if entry.IsDir && !entry.IsSymlink && (opts.MaxDepth <= 0 || entry.Depth < opts.MaxDepth) {
				if err := walk(entry.Rel); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(".")
}

func hasAnySkippedPrefix(rel string, skipped []string) bool {
	for _, sp := range skipped {
		if strings.HasPrefix(rel, sp+"/") {
			return true
		}
	}
	return false
}

func surveySkipsOverlayDir(name string, opts sandbox.SurveyOptions) bool {
	return (!opts.IncludeVCSMetadata && sandbox.IsVCSDirBaseName(name)) ||
		(!opts.IncludeEngineOverlay && sandbox.IsEngineOverlayBaseName(name))
}
