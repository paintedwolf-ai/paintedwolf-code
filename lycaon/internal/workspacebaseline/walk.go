package workspacebaseline

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// Branch walks the prepared workspace with the worker's file inclusion policy.
// Git ignore rules and scanner admission do not participate.
//
// Each captured file carries its structured identity: the root it lives under
// and the path relative to that root. Single-root workspaces keep the legacy
// spelling where the identity is the display path itself.
func Branch(roots []projectroot.RootRef, branch string) Walk {
	return func(ctx context.Context, visit func(CaptureFile) error) error {
		if len(roots) <= 1 {
			rootID := ""
			if len(roots) == 1 {
				rootID = roots[0].ID
			}
			return walkRoot(ctx, branch, rootID, func(rel string) string { return rel }, visit)
		}
		primary, err := projectroot.PrimaryRoot(roots)
		if err != nil {
			return err
		}
		for _, root := range roots {
			dir, err := projectroot.BranchDirForID(root.ID)
			if err != nil {
				return err
			}
			// Display paths stay primary-relative: readers parse them with
			// primary-relative semantics.
			qualify := func(path string) string {
				return projectroot.Qualify(roots, primary.ID, root, filepath.Join(root.Path, filepath.FromSlash(path)))
			}
			if err := walkRoot(ctx, filepath.Join(branch, dir), root.ID, qualify, visit); err != nil {
				return err
			}
		}
		return nil
	}
}

func walkRoot(ctx context.Context, root string, rootID string, qualify func(string) string, visit func(CaptureFile) error) error {
	opened, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = opened.Close() }()
	walker := branchWalker{ctx: ctx, root: opened, abs: root, rootID: rootID, qualify: qualify, visit: visit}
	return walker.directory(".")
}

type branchWalker struct {
	ctx     context.Context
	root    *os.Root
	abs     string
	rootID  string
	qualify func(string) string
	visit   func(CaptureFile) error
}

func (w branchWalker) directory(rel string) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	dir, err := w.root.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	for {
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if err := w.ctx.Err(); err != nil {
				return err
			}
			path := filepath.ToSlash(filepath.Join(rel, entry.Name()))
			if entry.IsDir() {
				if sandbox.ShouldSkipDir(path, entry.Name()) {
					continue
				}
				if err := w.directory(path); err != nil {
					return err
				}
				continue
			}
			abs := filepath.Join(w.abs, filepath.FromSlash(path))
			if sandbox.IsGitlinkDirEntry(entry.Name(), abs, entry) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if err := w.visit(CaptureFile{RootID: w.rootID, ScopeRel: path, Path: w.qualify(path), Abs: abs, Info: info}); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
