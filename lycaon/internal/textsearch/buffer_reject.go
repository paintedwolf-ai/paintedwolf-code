package textsearch

import (
	"bytes"
	"context"
)

// bufferRejector proves that complete buffered records cannot select a line.
// The record reader retains authority over partial records and every refusal.
type bufferRejector struct {
	literal literalEngine
	vm      bool
}

func newBufferRejector(sel Selector, opts ScanOptions) *bufferRejector {
	if opts.Invert || opts.Before > 0 || opts.After > 0 {
		return nil
	}
	m, ok := sel.(*Matcher)
	if !ok || m.none {
		return nil
	}
	r := &bufferRejector{}
	switch e := m.engine.(type) {
	case literalEngine:
		r.literal = e
	case vmEngine:
		if e.literal == nil {
			return nil
		}
		r.literal, r.vm = *e.literal, true
		r.literal.limits = e.limits
	default:
		return nil
	}
	for _, p := range r.literal.patterns {
		if len(p) == 0 {
			return nil
		}
	}
	return r
}

func (r *bufferRejector) skip(ctx context.Context, input *scanRecordReader, separator byte, start, maximum int64) (int, int) {
	// Peek only fills when empty; it never reads beyond the admitted reader buffer.
	if input.reader.Buffered() == 0 {
		if _, err := input.reader.Peek(1); err != nil {
			return 0, 0
		}
	}
	buffered, _ := input.reader.Peek(input.reader.Buffered())
	end := bytes.LastIndexByte(buffered, separator) + 1
	if end == 0 {
		return 0, 0
	}
	block := buffered[:end]
	n := int64(len(block))
	limits := r.literal.limits
	if maximum > 0 && n > maximum-start || n > limits.InputBytes {
		return 0, 0
	}
	if r.vm && (n > limits.WorkSteps || n*12+8 > limits.MemoryBytes) {
		return 0, 0
	}
	// A chunk bound also bounds each record's proof and comparison work.
	work := n
	for _, p := range r.literal.patterns {
		cost := (n + 1) * int64(len(p))
		if cost > limits.WorkSteps-work {
			return 0, 0
		}
		work += cost
	}
	if r.literal.fold {
		if _, ok := literalRecordProof(ctx, block, true, false); !ok {
			return 0, 0
		}
	}
	if r.vm && r.literal.line && separator == 0 && bytes.ContainsRune(block, '\n') {
		return 0, 0
	}
	for _, p := range r.literal.patterns {
		if r.literal.fold {
			charged := int64(0)
			at, err := r.literal.foldedIndex(ctx, block, p, 0, &charged)
			if err != nil || at >= 0 {
				return 0, 0
			}
		} else if bytes.Contains(block, p) {
			return 0, 0
		}
	}
	if ctx.Err() != nil {
		return 0, 0
	}
	records := bytes.Count(block, []byte{separator})
	_, _ = input.reader.Discard(end)
	return end, records
}
