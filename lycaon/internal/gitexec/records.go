package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// MaxRecordBytes bounds one NUL-delimited protocol record, not the stream.
const MaxRecordBytes = 1 << 20

// RunRecords delivers NUL-delimited stdout records; stderr remains diagnostic.
// Record slices expire after the callback; errors invalidate accumulated results.
func RunRecords(ctx context.Context, dir string, args []string, opts Opts, consume func([]byte) error) error {
	if consume == nil {
		return errors.New("git record consumer is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &recordWriter{ctx: ctx, consume: consume, cancel: cancel}
	diagnostics, code, err := run(ctx, dir, args, opts, w)
	if w.err != nil {
		return w.err
	}
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git enumeration exited %d: %s", code, diagnostics)
	}
	if len(w.pending) != 0 {
		return errors.New("git enumeration ended inside a record")
	}
	return nil
}

type recordWriter struct {
	ctx     context.Context
	consume func([]byte) error
	cancel  context.CancelFunc
	pending []byte
	err     error
}

func (w *recordWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 && w.err == nil {
		if err := w.ctx.Err(); err != nil {
			w.fail(err)
			break
		}
		end := bytes.IndexByte(p, 0)
		chunk := len(p)
		if end >= 0 {
			chunk = end
		}
		if len(w.pending)+chunk > MaxRecordBytes {
			w.fail(fmt.Errorf("git record exceeds %d bytes", MaxRecordBytes))
			break
		}
		w.pending = append(w.pending, p[:chunk]...)
		if end < 0 {
			break
		}
		if err := w.consume(w.pending); err != nil {
			w.fail(err)
			break
		}
		w.pending = w.pending[:0]
		p = p[end+1:]
	}
	return n, nil
}

func (w *recordWriter) fail(err error) {
	w.err = err
	w.cancel()
}
