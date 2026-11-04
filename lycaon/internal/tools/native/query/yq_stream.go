package query

import (
	"context"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

// reader leaves stdin incremental. eval-all retains decoded documents because
// its expression may combine them; ordinary eval needs only the current one.
func (r *yqRun) reader(file commandprogram.Field) (io.Reader, string, error) {
	if file.Value == "-" {
		return &yqInput{ctx: r.inv.ctx, source: r.inv.input, maximum: safecmd.JQCaps().InputBytes}, "-", nil
	}
	captured, err := r.capture(file)
	if err != nil {
		return nil, file.Value, err
	}
	return strings.NewReader(captured.text), file.Value, nil
}

type yqInput struct {
	ctx               context.Context
	source            io.Reader
	maximum, observed int64
}

func (r *yqInput) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	p = p[:min(int64(len(p)), max(r.maximum-r.observed+1, 1))]
	n, err := r.source.Read(p)
	r.observed += int64(n)
	if r.observed > r.maximum {
		return max(n-1, 0), safecmd.Reject("JQ_INPUT_TOO_LARGE", map[string]any{"path": "<stdin>", "max_bytes": r.maximum})
	}
	return n, err
}
