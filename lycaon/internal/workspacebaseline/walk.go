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
func Branch(roots []projectroot.RootRef, branch string) Walk {
	return func(ctx context.Context, visit func(CaptureFile) error) error {
		if len(roots) <= 1 {
			return walkRoot(ctx, branch, func(path string) string { return path }, visit)
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
			qualify := func(path string) string {
				return projectroot.Qualify(primary, root, filepath.Join(root.Path, filepath.FromSlash(path)))
			}
			if err := walkRoot(ctx, filepath.Join(branch, dir), qualify, visit); err != nil {
				return err
			}
		}
		return nil
	}
}

func walkRoot(ctx context.Context, root string, qualify func(string) string, visit func(CaptureFile) error) error {
	opened, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = opened.Close() }()
	walker := branchWalker{ctx: ctx, root: opened, abs: root, qualify: qualify, visit: visit}
	return walker.directory(".")
}

type branchWalker struct {
	ctx     context.Context
	root    *os.Root
	abs     string
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
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if err := w.visit(CaptureFile{Path: w.qualify(path), Abs: filepath.Join(w.abs, filepath.FromSlash(path)), Info: info}); err != nil {
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
