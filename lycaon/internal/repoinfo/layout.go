package repoinfo

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

type layoutWalkState struct {
	fileCount       int
	allPaths        []string
	topLevelDirs    map[string]struct{}
	rootFiles       map[string]struct{}
	collectPaths    bool
	collectTopLevel bool
}

func newLayoutWalkState() *layoutWalkState {
	return &layoutWalkState{
		topLevelDirs:    make(map[string]struct{}),
		rootFiles:       make(map[string]struct{}),
		collectPaths:    true,
		collectTopLevel: true,
	}
}

func (s *layoutWalkState) noteFile(rel string) {
	s.fileCount++
	if s.collectPaths {
		s.allPaths = append(s.allPaths, rel)
	}
	if s.collectTopLevel {
		s.noteTopLevel(rel)
	}
	if s.fileCount > packboard.SmallMaxFiles {
		s.collectPaths = false
		s.allPaths = nil
	}
	if s.fileCount > packboard.MediumMaxFiles {
		s.collectTopLevel = false
		s.topLevelDirs = nil
		s.rootFiles = nil
	}
}

func (s *layoutWalkState) noteTopLevel(rel string) {
	rel = filepath.ToSlash(rel)
	if !strings.Contains(rel, "/") {
		s.rootFiles[rel] = struct{}{}
		return
	}
	dir := strings.SplitN(rel, "/", 2)[0] + "/"
	s.topLevelDirs[dir] = struct{}{}
}

func (s *layoutWalkState) finalize() api.RepoLayout {
	switch {
	case s.fileCount == 0:
		return api.RepoLayout{}
	case s.fileCount <= packboard.TinyMaxFiles:
		paths := append([]string(nil), s.allPaths...)
		sort.Strings(paths)
		return api.RepoLayout{Files: paths}
	case s.fileCount <= packboard.SmallMaxFiles:
		return api.RepoLayout{TopLevel: smallTopLevel(s.topLevelDirs, s.rootFiles)}
	case s.fileCount <= packboard.MediumMaxFiles:
		return api.RepoLayout{TopLevel: mediumTopLevel(s.topLevelDirs)}
	default:
		return api.RepoLayout{}
	}
}

func smallTopLevel(dirs map[string]struct{}, rootFiles map[string]struct{}) []string {
	out := make([]string, 0, len(dirs)+len(rootFiles))
	for dir := range dirs {
		out = append(out, dir)
	}
	for name := range rootFiles {
		if isPromotedRootFile(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func mediumTopLevel(dirs map[string]struct{}) []string {
	if len(dirs) == 0 {
		return nil
	}
	out := make([]string, 0, len(dirs))
	for dir := range dirs {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

func isPromotedRootFile(name string) bool {
	if strings.HasPrefix(name, "README") {
		return true
	}
	switch name {
	case "package.json", "go.mod", "Cargo.toml", "pyproject.toml", "Makefile":
		return true
	default:
		return false
	}
}
