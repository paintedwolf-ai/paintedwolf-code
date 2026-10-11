package projectsource

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// SourceDirEntry is one resolved child of a browsed directory.
type SourceDirEntry struct {
	Name  string
	IsDir bool
}

// SourceDirListing is one lazily loaded directory level.
type SourceDirListing struct {
	WorkspaceID   string
	RootID        string
	Dir           string
	WatchComplete bool
	Entries       []SourceDirEntry
}

type sourceBrowseRoot struct {
	root     projectroot.RootRef
	path     string
	resolved string
}

func resolveSourceBrowseRoot(p ProjectSource, rootID string) (sourceBrowseRoot, error) {
	if p == nil || len(p.SourceRoots()) == 0 {
		return sourceBrowseRoot{}, ErrSourceNoRoot
	}
	root, err := selectSingleSourceRoot(p, rootID)
	if err != nil {
		return sourceBrowseRoot{}, err
	}
	rootPath := strings.TrimSpace(root.Path)
	if rootPath == "" {
		return sourceBrowseRoot{}, ErrSourceNoRoot
	}
	info, err := os.Stat(rootPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sourceBrowseRoot{}, ErrSourceNotFound
		}
		return sourceBrowseRoot{}, fmt.Errorf("stat browse root: %w", err)
	}
	if !info.IsDir() {
		return sourceBrowseRoot{}, ErrSourceNotFound
	}
	resolved, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return sourceBrowseRoot{}, fmt.Errorf("resolve root: %w", err)
	}
	return sourceBrowseRoot{root: root, path: rootPath, resolved: resolved}, nil
}

func browseProjectSourceAt(root sourceBrowseRoot, dir string) (SourceDirListing, string, error) {
	relDir, err := normalizeSourceBrowseDir(dir)
	if err != nil {
		return SourceDirListing{}, "", err
	}
	dirAbs, resolved, err := resolveSourceDir(root.path, root.resolved, relDir)
	if err != nil {
		return SourceDirListing{}, "", err
	}
	children, err := sandbox.SurveyReadDir(dirAbs, relDir, sandbox.HumanFilesSurveyOptions())
	if err != nil {
		return SourceDirListing{}, "", fmt.Errorf("browse source dir: %w", err)
	}
	return SourceDirListing{RootID: root.root.ID, Dir: relDir, Entries: sourceListingEntries(children)}, resolved, nil
}

func sourceListingEntries(children []sandbox.SurveyEntry) []SourceDirEntry {
	entries := make([]SourceDirEntry, 0, len(children))
	for _, child := range children {
		if repochange.IsPrivatePath(child.Abs) {
			continue
		}
		isDir := child.IsDir
		if child.IsSymlink {
			info, statErr := os.Stat(child.Abs)
			if statErr != nil {
				continue
			}
			isDir = info.IsDir()
		}
		entries = append(entries, SourceDirEntry{Name: filepath.Base(child.Abs), IsDir: isDir})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries
}

func normalizeSourceBrowseDir(dir string) (string, error) {
	trimmed := strings.TrimSpace(dir)
	if strings.HasPrefix(filepath.ToSlash(trimmed), "/") || sandbox.HasParentTraversal(trimmed) {
		return "", ErrSourcePathDenied
	}
	return normalizeSourceDir(trimmed), nil
}

// selectSingleSourceRoot returns one explicit or primary destination.
func selectSingleSourceRoot(p ProjectSource, rootID string) (projectroot.RootRef, error) {
	if strings.TrimSpace(rootID) != "" {
		for _, root := range p.SourceRoots() {
			if root.ID == rootID {
				return root, nil
			}
		}
		return projectroot.RootRef{}, ErrSourceNotFound
	}
	for _, root := range p.SourceRoots() {
		if root.IsPrimary {
			return root, nil
		}
	}
	return projectroot.RootRef{}, ErrSourceNoRoot
}

// normalizeSourceDir maps empty paths to the root folder.
func normalizeSourceDir(dir string) string {
	rel := strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(strings.TrimSpace(dir))), "/")
	if rel == "" {
		return "."
	}
	return rel
}

// resolveSourceDir rejects textual and symlink escapes.
func resolveSourceDir(rootPath, evalRoot, relDir string) (string, string, error) {
	abs := filepath.Join(rootPath, filepath.FromSlash(relDir))
	if !withinRoot(rootPath, abs) {
		return "", "", ErrSourcePathDenied
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", ErrSourceNotFound
		}
		return "", "", fmt.Errorf("stat browse dir: %w", err)
	}
	if !info.IsDir() {
		return "", "", ErrSourceNotFound
	}
	evalAbs, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", fmt.Errorf("resolve browse dir: %w", err)
	}
	if !withinRoot(evalRoot, evalAbs) {
		return "", "", ErrSourcePathDenied
	}
	return abs, filepath.Clean(evalAbs), nil
}

// withinRoot expects absolute, symlink-resolved paths.
func withinRoot(rootPath, abs string) bool {
	rel, err := filepath.Rel(rootPath, abs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
