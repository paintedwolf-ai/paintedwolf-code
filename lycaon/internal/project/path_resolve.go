package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProjectPathEntryKind is the navigable filesystem identity of a project path.
type ProjectPathEntryKind string

const (
	ProjectPathFile   ProjectPathEntryKind = "file"
	ProjectPathFolder ProjectPathEntryKind = "folder"
)

// ErrSourcePathAmbiguous means an unpinned relative path exists in multiple roots.
var ErrSourcePathAmbiguous = errors.New("source path ambiguous")

// ResolvedProjectPath is one canonical root-addressed project path.
type ResolvedProjectPath struct {
	info      os.FileInfo
	RootID    string
	Path      string
	EntryKind ProjectPathEntryKind
}

// ResolveProjectPath validates one file or folder against the project source
// jail without reading file content. An omitted rootID succeeds only when one
// attached root contains the path.
func ResolveProjectPath(p *Project, rootID, pathQuery string) (ResolvedProjectPath, error) {
	if p == nil || len(p.Roots) == 0 {
		return ResolvedProjectPath{}, ErrSourceNoRoot
	}
	rel, err := normalizeNavigableProjectPath(pathQuery)
	if err != nil {
		return ResolvedProjectPath{}, err
	}
	roots := orderedRootsForSource(p)
	if strings.TrimSpace(rootID) != "" {
		roots = nil
		for _, root := range p.Roots {
			if root.ID == rootID {
				roots = append(roots, root)
				break
			}
		}
		if len(roots) == 0 {
			return ResolvedProjectPath{}, ErrSourceNotFound
		}
	}

	matches := make([]ResolvedProjectPath, 0, 1)
	denied := false
	for _, root := range roots {
		resolved, resolveErr := resolveProjectPathInRoot(root, rel)
		switch {
		case resolveErr == nil:
			matches = append(matches, resolved)
		case errors.Is(resolveErr, ErrSourcePathDenied):
			denied = true
		case errors.Is(resolveErr, ErrSourceNotFound):
			continue
		default:
			return ResolvedProjectPath{}, resolveErr
		}
	}
	if len(matches) > 1 {
		return ResolvedProjectPath{}, ErrSourcePathAmbiguous
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if denied {
		return ResolvedProjectPath{}, ErrSourcePathDenied
	}
	return ResolvedProjectPath{}, ErrSourceNotFound
}

func normalizeNavigableProjectPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	for strings.HasPrefix(trimmed, "./") {
		trimmed = strings.TrimPrefix(trimmed, "./")
	}
	trimmed = strings.TrimSuffix(trimmed, "/")
	if trimmed == "" || strings.ContainsRune(trimmed, 0) || filepath.IsAbs(trimmed) {
		return "", ErrSourcePathInvalid
	}
	segments := strings.Split(trimmed, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrSourcePathDenied
		}
	}

	return strings.Join(segments, "/"), nil
}

func resolveProjectPathInRoot(root Root, rel string) (ResolvedProjectPath, error) {
	rootPath := strings.TrimSpace(root.Path)
	if rootPath == "" {
		return ResolvedProjectPath{}, ErrSourceNotFound
	}
	rootAbs, err := filepath.Abs(rootPath)
	if err != nil {
		return ResolvedProjectPath{}, fmt.Errorf("resolve project root: %w", err)
	}
	candidate := filepath.Join(rootAbs, filepath.FromSlash(rel))
	if !withinRoot(rootAbs, candidate) {
		return ResolvedProjectPath{}, ErrSourcePathDenied
	}
	info, err := os.Stat(candidate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ResolvedProjectPath{}, ErrSourceNotFound
		}
		return ResolvedProjectPath{}, fmt.Errorf("stat project path: %w", err)
	}
	evalRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return ResolvedProjectPath{}, fmt.Errorf("resolve project root links: %w", err)
	}
	evalCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return ResolvedProjectPath{}, fmt.Errorf("resolve project path links: %w", err)
	}
	if !withinRoot(evalRoot, evalCandidate) {
		return ResolvedProjectPath{}, ErrSourcePathDenied
	}

	var kind ProjectPathEntryKind
	switch {
	case info.IsDir():
		kind = ProjectPathFolder
	case info.Mode().IsRegular():
		kind = ProjectPathFile
	default:
		return ResolvedProjectPath{}, ErrSourceNotFound
	}
	return ResolvedProjectPath{RootID: root.ID, Path: filepath.ToSlash(rel), EntryKind: kind, info: info}, nil
}
