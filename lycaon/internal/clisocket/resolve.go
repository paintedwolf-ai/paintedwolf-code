// Package clisocket serves CLI commands over a Unix domain socket.
package clisocket

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

// canonical resolves symlinks and cleans a path for equality comparison.
func canonical(path string) string {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return filepath.Clean(path)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}

// matchKind ranks how a project root relates to the requested path.
type matchKind int

const (
	matchNone matchKind = iota
	matchAncestor
	matchExact
)

type pathMatch struct {
	project project.Project
	kind    matchKind
	rootLen int
}

// ResolvePath matches a path against existing project roots, preferring exact matches over ancestors.
func ResolvePath(projects []project.Project, path string) api.CLIOpenEvent {
	want := canonical(path)
	var best *pathMatch
	for _, p := range projects {
		for _, r := range p.Roots {
			kind, rootLen := classify(canonical(r.Path), want)
			if kind == matchNone {
				continue
			}
			candidate := pathMatch{project: p, kind: kind, rootLen: rootLen}
			if best == nil || better(candidate, *best) {
				winner := candidate
				best = &winner
			}
		}
	}
	if best == nil {
		return api.CLIOpenEvent{Action: api.CLIOpenActionCreate, Path: want}
	}
	id := best.project.ID
	return api.CLIOpenEvent{Action: api.CLIOpenActionOpen, ProjectID: &id, Path: want}
}

// classify reports how want sits relative to root, both already canonical.
//
// filepath.Rel rather than a string prefix: root /a/bc must not swallow
// /a/bcd, and Rel reports that as an escape where HasPrefix would not.
func classify(root, want string) (matchKind, int) {
	if root == want {
		return matchExact, len(root)
	}
	rel, err := filepath.Rel(root, want)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return matchNone, 0
	}
	return matchAncestor, len(root)
}

// better ranks exact and deeper path matches first.
func better(a, b pathMatch) bool {
	if a.kind != b.kind {
		return a.kind > b.kind
	}
	if a.kind == matchAncestor && a.rootLen != b.rootLen {
		return a.rootLen > b.rootLen
	}
	return a.project.LastOpenedAt.After(b.project.LastOpenedAt)
}

// DisplayName returns the project name or primary folder name.
func DisplayName(p project.Project) string {
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	for _, r := range p.Roots {
		if r.IsPrimary {
			return filepath.Base(r.Path)
		}
	}
	return ""
}

// ResolveName returns every match, most recently opened first.
func ResolveName(projects []project.Project, name string) []project.Project {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return nil
	}
	var out []project.Project
	for i := range projects {
		if strings.EqualFold(DisplayName(projects[i]), want) {
			out = append(out, projects[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastOpenedAt.After(out[j].LastOpenedAt)
	})
	return out
}

// LooksLikePath decides whether an `open` argument names a folder or a project.
//
// A separator or a leading ~ settles it without touching the disk. A bare word is
// checked against the filesystem so `lycaon open myrepo` works from the parent
// directory, and falls through to name lookup when no such directory is there.
func LooksLikePath(arg string) bool {
	if arg == "." || arg == ".." || strings.HasPrefix(arg, "~") {
		return true
	}
	if strings.ContainsRune(arg, filepath.Separator) {
		return true
	}
	info, err := os.Stat(arg)
	return err == nil && info.IsDir()
}
