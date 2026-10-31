package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Each active directory retains at most one page; empty directories do not
// accumulate in a plan. Descriptor-relative reads keep traversal in its root.
func walkMutationTree(ctx context.Context, root *fseffect.ReadRoot, base, rel string, visit, after func(string, os.FileInfo) error, memory *toolcontract.ProgramMemory) error {
	// Retain the current page, its replacement page, entry metadata and recursive
	// frame before ReadDir allocates. The enclosing program bounds total depth.
	release, err := memory.ReserveProgramState(ctx, 2*128*int64(2048+len(base)+len(rel)))
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := root.Open(filepath.Join(base, rel))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("read %s: directory changed", rel)
	}
	if err := visit(rel, info); err != nil {
		return err
	}
	for {
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			child := filepath.Join(rel, entry.Name())
			if info.IsDir() {
				if err := walkMutationTree(ctx, root, base, child, visit, after, memory); err != nil {
					return err
				}
			} else if err := visit(child, info); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if after != nil {
		return after(rel, info)
	}
	return nil
}
