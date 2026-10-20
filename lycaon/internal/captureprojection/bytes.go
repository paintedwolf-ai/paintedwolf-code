package captureprojection

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// Bytes is a byte-exact screened projection, with the usual capture provenance.
type Bytes struct {
	Value    []byte
	Metadata Metadata
}

// LimitError refuses work before a projection's declared resource envelope.
type LimitError struct {
	Resource          string
	Maximum, Observed int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("%s: requested %d, maximum %d", e.Resource, e.Observed, e.Maximum)
}

// BytesSpans projects one bounded detector unit. Larger streams use ScreenStream;
// slicing independent units at a caller's chunk boundaries is not screening.
func (p *Projector) BytesSpans(ctx context.Context, scope Scope, label string, value []byte) (Bytes, []Span, error) {
	if len(value) > secretmatch.MaxScreenWindowBytes {
		return Bytes{}, nil, &LimitError{"screen_input_bytes", secretmatch.MaxScreenWindowBytes, int64(len(value))}
	}
	if ctx != nil && ctx.Err() != nil {
		return Bytes{}, nil, ctx.Err()
	}
	safe, spans, err := p.TextSpans(ctx, scope, label, string(value))
	if err != nil {
		return Bytes{}, nil, err
	}
	return Bytes{Value: []byte(safe.Value), Metadata: safe.Metadata}, spans, nil
}
