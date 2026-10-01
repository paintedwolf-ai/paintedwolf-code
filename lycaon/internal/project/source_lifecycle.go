package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

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
func locateLifecycleSource(p *Project, rootID, pathQuery string) (Root, string, string, error) {
	if pathQuery == "" || isSourceRootQuery(pathQuery) {
		return Root{}, "", "", ErrSourcePathInvalid
	}
	rootID = strings.TrimSpace(rootID)
	if rootID != "" {
		root, err := selectSingleSourceRoot(p, rootID)
		if err != nil {
			return Root{}, "", "", err
		}
		abs, rel, err := resolveLifecycleExisting(root, pathQuery)
		if err == nil {
			return root, abs, rel, nil
		}
		if !errors.Is(err, ErrSourceNotFound) {
			return Root{}, "", "", err
		}
		if sourceExistsOnOtherRoot(p, root.ID, pathQuery) {
			return Root{}, "", "", ErrSourceCrossRoot
		}
		return Root{}, "", "", ErrSourceNotFound
	}
	normalized := false
	for _, root := range orderedRootsForSource(p) {
		abs, rel, resolveErr := resolveLifecyclePath(root.Path, pathQuery)
		if resolveErr != nil {
			continue
		}
		normalized = true
		if isSourceRootRel(rel) {
			return Root{}, "", "", ErrSourcePathInvalid
		}
		if isReservedSourceRel(rel) {
			return Root{}, "", "", ErrSourcePathProtected
		}
		if _, err := os.Lstat(abs); err == nil {
			return root, abs, rel, nil
		} else if !os.IsNotExist(err) {
			return Root{}, "", "", fmt.Errorf("stat source: %w", err)
		}
	}
	if !normalized {
		return Root{}, "", "", ErrSourcePathDenied
	}
	return Root{}, "", "", ErrSourceNotFound
}

func sourceExistsOnOtherRoot(p *Project, pinnedID, pathQuery string) bool {
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

func resolveLifecycleExisting(root Root, pathQuery string) (abs, rel string, err error) {
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

func resolveLifecycleDest(root Root, pathQuery string) (abs, rel string, err error) {
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
