package projectsource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
)

var (
	// ErrSourceCrossRoot rejects mutations across attached roots.
	ErrSourceCrossRoot = errors.New("source cross root")
	// ErrSourceNotEmpty rejects non-recursive directory deletion.
	ErrSourceNotEmpty = errors.New("source not empty")
	// ErrSourceTrashFailed reports an unsuccessful Trash move.
	ErrSourceTrashFailed = errors.New("source trash failed")
)

// SourceLifecycleResult reports a lifecycle target.
type SourceLifecycleResult struct {
	RootID string
	Path   string
}

// SourceRenamePlan is a validated same-root move.
type SourceRenamePlan struct {
	OperationID      string
	RootID, From, To string
}

// SourceRenameRequest is one same-root rename or move.
type SourceRenameRequest struct {
	RootID    string
	From      string
	To        string
	Prepare   func(context.Context, SourceRenamePlan) error
	SessionID string
	Turn      int
}

// SourceCopyRequest is one same-root recursive copy of a file or folder.
type SourceCopyRequest struct {
	RootID    string
	From      string
	To        string
	SessionID string
	Turn      int
}

// SourceDeleteRequest is one Trash move of a file or folder.
type SourceDeleteRequest struct {
	RootID    string
	Path      string
	Recursive bool
	SessionID string
	Turn      int
}

func sourceEntryKind(info os.FileInfo) SourceEntryKind {
	if info.IsDir() {
		return SourceEntryFolder
	}
	return SourceEntryFile
}

// locateLifecycleSource resolves an existing path within one root.
func locateLifecycleSource(p ProjectSource, rootID, pathQuery string) (projectroot.RootRef, string, string, error) {
	if pathQuery == "" || isSourceRootQuery(pathQuery) {
		return projectroot.RootRef{}, "", "", ErrSourcePathInvalid
	}
	rootID = strings.TrimSpace(rootID)
	if rootID != "" {
		root, err := selectSingleSourceRoot(p, rootID)
		if err != nil {
			return projectroot.RootRef{}, "", "", err
		}
		abs, rel, err := resolveLifecycleExisting(root, pathQuery)
		if err == nil {
			return root, abs, rel, nil
		}
		if !errors.Is(err, ErrSourceNotFound) {
			return projectroot.RootRef{}, "", "", err
		}
		if sourceExistsOnOtherRoot(p, root.ID, pathQuery) {
			return projectroot.RootRef{}, "", "", ErrSourceCrossRoot
		}
		return projectroot.RootRef{}, "", "", ErrSourceNotFound
	}
	normalized := false
	for _, root := range orderedRootsForSource(p) {
		abs, rel, resolveErr := resolveLifecyclePath(root.Path, pathQuery)
		if resolveErr != nil {
			continue
		}
		normalized = true
		if isSourceRootRel(rel) {
			return projectroot.RootRef{}, "", "", ErrSourcePathInvalid
		}
		if isReservedSourceRel(rel) {
			return projectroot.RootRef{}, "", "", ErrSourcePathProtected
		}
		if _, err := os.Lstat(abs); err == nil {
			return root, abs, rel, nil
		} else if !os.IsNotExist(err) {
			return projectroot.RootRef{}, "", "", fmt.Errorf("stat source: %w", err)
		}
	}
	if !normalized {
		return projectroot.RootRef{}, "", "", ErrSourcePathDenied
	}
	return projectroot.RootRef{}, "", "", ErrSourceNotFound
}

func sourceExistsOnOtherRoot(p ProjectSource, pinnedID, pathQuery string) bool {
	for _, root := range orderedRootsForSource(p) {
		if root.ID == pinnedID {
			continue
		}
		if _, _, err := resolveLifecycleExisting(root, pathQuery); err == nil {
			return true
		}
	}
	return false
}

func resolveLifecycleExisting(root projectroot.RootRef, pathQuery string) (abs, rel string, err error) {
	abs, rel, err = resolveLifecyclePath(root.Path, pathQuery)
	if err != nil {
		return "", "", err
	}
	if isSourceRootRel(rel) {
		return "", "", ErrSourcePathInvalid
	}
	if isReservedSourceRel(rel) {
		return "", "", ErrSourcePathProtected
	}
	if _, err := os.Lstat(abs); err != nil {
		if os.IsNotExist(err) {
			return "", "", ErrSourceNotFound
		}
		return "", "", fmt.Errorf("stat source: %w", err)
	}
	return abs, rel, nil
}

func resolveLifecycleDest(root projectroot.RootRef, pathQuery string) (abs, rel string, err error) {
	if pathQuery == "" || isSourceRootQuery(pathQuery) {
		return "", "", ErrSourcePathInvalid
	}
	abs, rel, err = resolveLifecyclePath(root.Path, pathQuery)
	if err != nil {
		return "", "", err
	}
	if isSourceRootRel(rel) {
		return "", "", ErrSourcePathInvalid
	}
	if isReservedSourceRel(rel) {
		return "", "", ErrSourcePathProtected
	}
	if _, err := os.Lstat(abs); err == nil {
		return "", "", ErrSourceExists
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("stat source: %w", err)
	}
	return abs, rel, nil
}

func isSourceRootQuery(pathQuery string) bool {
	return pathQuery == "." || pathQuery == "./"
}

func isSourceRootRel(rel string) bool {
	return rel == "." || rel == ""
}

func isReservedSourceRel(rel string) bool {
	rel = filepath.ToSlash(rel)
	return sandbox.ShouldSkipDir(rel, path.Base(rel))
}

// SourceTrashFailedError carries the OS error from a failed Trash move.
type SourceTrashFailedError struct {
	Cause error
}

func (e *SourceTrashFailedError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrSourceTrashFailed.Error()
	}
	return e.Cause.Error()
}

func (e *SourceTrashFailedError) Unwrap() error {
	return ErrSourceTrashFailed
}

// Parent resolution preserves the selected symbolic link.
func sourceRelativePath(rootPath, absolute string) (string, error) {
	root, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, filepath.Join(parent, filepath.Base(absolute)))
	if err != nil {
		return "", err
	}
	if !filepath.IsLocal(rel) {
		return "", ErrSourcePathDenied
	}
	return rel, nil
}

var ErrSourcePathProtected = errors.New("source path is protected project metadata")

// Resolve parents without following the selected directory entry.
func resolveLifecyclePath(rootPath, query string) (string, string, error) {
	if query == "" || strings.ContainsRune(query, 0) || sandbox.HasParentTraversal(query) {
		return "", "", ErrSourcePathDenied
	}
	root, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve attached folder: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	clean := filepath.Clean(query)
	parent := filepath.Dir(clean)
	if !filepath.IsAbs(parent) {
		parent = filepath.Join(root, parent)
	}
	parent, err = resolveLifecycleParent(parent)
	if err != nil {
		return "", "", err
	}
	abs := filepath.Join(parent, filepath.Base(clean))
	rel, err := filepath.Rel(root, abs)
	if err != nil || !withinRoot(root, abs) {
		return "", "", ErrSourcePathDenied
	}
	return abs, filepath.ToSlash(rel), nil
}

// Missing destination directories remain in the resolved path.
func resolveLifecycleParent(parent string) (string, error) {
	current, tail := parent, ""
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(resolved, tail), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve file parent: %w", err)
		}
		// A dangling link cannot serve as a destination directory.
		if _, statErr := os.Lstat(current); statErr == nil {
			return "", ErrSourcePathDenied
		}
		next := filepath.Dir(current)
		if next == current {
			return "", err
		}
		tail = filepath.Join(filepath.Base(current), tail)
		current = next
	}
}
