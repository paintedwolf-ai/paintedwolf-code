package repomap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// ErrWalkStop signals WalkSourceFiles should stop without error (match cap, etc.).
var ErrWalkStop = errors.New("repomap: walk stop")

// SourceWalkOptions configures WalkSourceFiles.
type SourceWalkOptions struct {
	Root         string
	Subpaths     []string // repo-relative focus paths; empty = whole Root
	Recursive    bool
	MaxFiles     int
	MaxFileBytes int64
	PathIncluded func(relSlash string, isDir bool) bool
}

// SourceWalkStats reports walk progress and cap hits.
type SourceWalkStats struct {
	FilesScanned    int
	UnobservedFiles int
	FilesCapHit     bool
}

// WalkSourceFiles uses the shared survey boundary in deterministic path order.
// ErrWalkStop ends traversal without an error.
func WalkSourceFiles(ctx context.Context, opts SourceWalkOptions, visit func(relSlash, absPath string) error) (SourceWalkStats, error) {
	var stats SourceWalkStats
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return stats, fmt.Errorf("repomap: root required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return stats, fmt.Errorf("repomap: abs root: %w", err)
	}
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultWalkMaxFiles
	}
	maxBytes := opts.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = DefaultWalkMaxFileBytes
	}

	subpaths := opts.Subpaths
	if len(subpaths) == 0 {
		subpaths = []string{"."}
	}
	cleaned := make([]string, 0, len(subpaths))
	for _, sub := range subpaths {
		sub = strings.TrimSpace(sub)
		if sub == "" {
			sub = "."
		}
		cleaned = append(cleaned, filepath.ToSlash(sub))
	}
	sort.Strings(cleaned)
	cleaned = dedupeSorted(cleaned)

	repoRel := func(abs string) string {
		rel, err := filepath.Rel(absRoot, abs)
		if err != nil {
			return filepath.ToSlash(abs)
		}
		return filepath.ToSlash(rel)
	}
	admit := func(_, abs string, isDir bool) bool {
		return opts.PathIncluded == nil || opts.PathIncluded(repoRel(abs), isDir)
	}
	walkOpts := sandbox.SurveyOptions{IncludeHidden: true, Admit: admit}
	if !opts.Recursive {
		walkOpts.MaxDepth = 1
	}

	stopped := false
	for _, sub := range cleaned {
		if stopped {
			break
		}
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		walkRoot := absRoot
		if sub != "." {
			walkRoot = filepath.Join(absRoot, filepath.FromSlash(sub))
		}
		info, err := os.Stat(walkRoot)
		if err != nil {
			if os.IsNotExist(err) {
				stats.UnobservedFiles++
				continue
			}
			return stats, err
		}
		if !info.IsDir() {
			rel := repoRel(walkRoot)
			if !admit(rel, walkRoot, false) {
				continue
			}
			if info.Size() > maxBytes {
				stats.UnobservedFiles++
				continue
			}
			if stats.FilesScanned >= maxFiles {
				stats.FilesCapHit = true
				break
			}
			stats.FilesScanned++
			if err := visit(rel, walkRoot); err != nil {
				if errors.Is(err, ErrWalkStop) {
					break
				}
				return stats, err
			}
			continue
		}

		walkErr := sandbox.SurveyWalk(ctx, walkRoot, walkOpts, func(e sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			if e.IsDir {
				return sandbox.SurveyContinue, nil
			}
			if stats.FilesScanned >= maxFiles {
				stats.FilesCapHit = true
				stopped = true
				return sandbox.SurveyStop, nil
			}
			fi, err := e.DirEntry.Info()
			if err != nil {
				stats.UnobservedFiles++
				return sandbox.SurveyContinue, nil
			}
			if fi.Size() > maxBytes {
				stats.UnobservedFiles++
				return sandbox.SurveyContinue, nil
			}
			stats.FilesScanned++
			if err := visit(repoRel(e.Abs), e.Abs); err != nil {
				if errors.Is(err, ErrWalkStop) {
					stopped = true
					return sandbox.SurveyStop, nil
				}
				return sandbox.SurveyContinue, err
			}
			return sandbox.SurveyContinue, nil
		})
		if walkErr != nil {
			return stats, walkErr
		}
	}
	return stats, nil
}

func dedupeSorted(ss []string) []string {
	if len(ss) == 0 {
		return ss
	}
	out := ss[:1]
	for _, s := range ss[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
