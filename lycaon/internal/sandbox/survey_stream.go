package sandbox

import (
	"errors"
	"io"
	"os"
)

// Streaming keeps directory width out of memory; traversal still owns one batch
// and descriptor per ancestor. Capture manifests impose their own stable order.
func (w *surveyWalker) walkStreamingDir(rel, abs string, depth int) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	file, err := os.Open(abs)
	if err != nil {
		return w.readFailure(rel, err)
	}
	defer func() { _ = file.Close() }()
	dirDepth := w.budget.EnterDir()
	defer w.budget.LeaveDir()
	for {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		entries, readErr := file.ReadDir(256)
		if err := w.visitEntries(rel, abs, depth, dirDepth, entries); err != nil {
			return err
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return w.readFailure(rel, readErr)
		}
	}
}

func (w *surveyWalker) readFailure(rel string, err error) error {
	if canceled := w.ctx.Err(); canceled != nil {
		return canceled
	}
	if rel == "." || w.opts.OnBoundary == nil {
		return err
	}
	w.boundary(rel, BoundaryUnreadable, err.Error(), 0)
	return nil
}
