package commandprogram

import (
	"bytes"
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type substitutionDepthKey struct{}

// SubstitutionDepth is the number of substitutions enclosing ctx. The
// evaluator passes the Substitute context into nested expansion unchanged.
func SubstitutionDepth(ctx context.Context) int {
	depth, _ := ctx.Value(substitutionDepthKey{}).(int)
	return depth
}

// substitute runs one reached unit and returns its scalar output: trailing LF
// bytes stripped, NUL refused. Ordinary nonzero status is data for the
// evaluator; a port error or host stop fails the containing word.
func (e *WordExpander) substitute(r *expansionRun, part commandsurface.SubstitutionPart) (string, error) {
	depth := SubstitutionDepth(r.ctx) + 1
	if int64(depth) > e.limits.substitutionDepth {
		return "", e.limits.exceeded("substitution_depth", int64(depth), part.Span)
	}
	output := &substitutionOutput{run: r, maximum: e.limits.substitutionBytes, span: part.Span}
	ctx := context.WithValue(r.ctx, substitutionDepthKey{}, depth)
	result, err := e.substitutions.Substitute(ctx, r.scope, SubstitutionRequest{Unit: part, Depth: depth}, output)
	if refusal := output.refusal(); refusal != nil {
		return "", refusal
	}
	if err == nil {
		err = result.Validate()
	}
	if err != nil {
		return "", &SubstitutionError{Node: part.ID, Span: part.Span, Result: result, Cause: err}
	}
	if result.Terminal != toolcontract.ProducerCompleted || result.StopCode != "" {
		return "", &SubstitutionError{Node: part.ID, Span: part.Span, Result: result}
	}
	text := output.trimmed()
	if bytes.IndexByte(text, 0) >= 0 {
		return "", invalidValue(part.Span, "substitution output without NUL bytes")
	}
	if err := r.charge(int64(len(text))); err != nil {
		return "", err
	}
	return string(text), nil
}

// substitutionOutput retains one unit's stdout under substitution_bytes and
// the run's memory admission; the first refused write latches.
type substitutionOutput struct {
	run     *expansionRun
	maximum int64
	span    toolcontract.SourceSpan

	mu      sync.Mutex
	buffer  bytes.Buffer
	refused error
}

func (o *substitutionOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.refused != nil {
		return 0, o.refused
	}
	observed := int64(o.buffer.Len()) + int64(len(p))
	if observed > o.maximum {
		o.refused = o.run.expander.limits.exceeded("substitution_bytes", observed, o.span)
		return 0, o.refused
	}
	if err := o.run.charge(int64(len(p))); err != nil {
		o.refused = err
		return 0, err
	}
	return o.buffer.Write(p)
}

func (o *substitutionOutput) refusal() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.refused
}

func (o *substitutionOutput) trimmed() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return bytes.TrimRight(o.buffer.Bytes(), "\n")
}
