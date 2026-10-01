package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

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
