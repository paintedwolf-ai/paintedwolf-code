package secretmatch

import (
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const streamReadBytes = 64 << 10
const referenceContextBytes = len(referenceTokenPrefix) + 36 + 2

// ScreenBytes screens complete streams using the existing catalog window
// schedule, global exact-match cursors and union rewrite. check is called before
// every emitted chunk and final settlement. observe must charge retained metadata.
// admission charges actual allocations against an aggregate ledger. Pure local
// reference callers may pass nil; production capture requires aggregate admission.
func (m *Matcher) ScreenBytes(ctx context.Context, src io.Reader, dst io.Writer, label string, bounds StreamBounds, admission StreamMemoryAdmission, check func() error, observe func(context.Context, StreamSpan) error) (result StreamResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil || src == nil || dst == nil || bounds.InputBytes <= 0 || bounds.OutputBytes <= 0 || bounds.MemoryBytes <= 0 || bounds.DetectorWork <= 0 {
		return result, errors.New("invalid stream screening contract")
	}
	memory := &streamMemory{maximum: bounds.MemoryBytes, ctx: ctx, admission: admission}
	defer func() { memory.release(memory.used) }()
	// Fixed hash/context/iterator/control payloads share the operation lease too.
	if err := memory.take(8192); err != nil {
		return result, err
	}
	ctx, revision, checkClass, err := m.streamClassifications(ctx, memory)
	if err != nil {
		return result, err
	}
	originalCheck := check
	check = func() error {
		if originalCheck != nil {
			if err := originalCheck(); err != nil {
				return err
			}
		}
		return checkClass()
	}
	needles, err := m.streamEvidence(ctx, memory)
	if err != nil {
		return result, err
	}
	longest := 1
	for _, n := range needles {
		longest = max(longest, len(n.needle.text))
	}
	window, overlap := m.screenWindowBytes(), m.screenOverlap()
	if window <= overlap || overlap < referenceContextBytes || window > MaxScreenWindowBytes {
		return result, errors.New("invalid detector window profile")
	}
	prefix := ""
	if strings.TrimSpace(label) != "" {
		if len(label) >= window {
			return result, &StreamLimitError{"screen_label_bytes", int64(window), int64(len(label)) + 1}
		}
		if err := memory.take(int64(len(label)) + 1); err != nil {
			return result, err
		}
		prefix = label + "="
	}
	if len(prefix) > window {
		return result, &StreamLimitError{"screen_label_bytes", int64(window), int64(len(prefix))}
	}
	state := streamScreen{matcher: m, memory: memory, needles: needles, hits: streamHits{memory: memory}, bounds: bounds, check: check, observe: observe}
	state.result.ClassificationRevision = revision
	if err := state.grow(len(prefix) + streamReadBytes); err != nil {
		return result, err
	}
	state.buffer = append(state.buffer, prefix...)
	state.inputOrigin = int64(len(prefix))
	state.emit = int64(len(prefix))
	if err := memory.take(streamReadBytes); err != nil {
		return result, err
	}
	chunk := make([]byte, streamReadBytes)
	eof, empty := false, 0
	for {
		if err := ctx.Err(); err != nil {
			return state.result, err
		}
		if check != nil {
			if err := check(); err != nil {
				return state.result, err
			}
		}
		// A complete detector window plus the largest exact spelling and token
		// context makes every candidate beginning in the next committed prefix whole.
		required := state.catalog + int64(window+longest+referenceContextBytes+utf8.UTFMax)
		if !eof && state.base+int64(len(state.buffer)) < required {
			readMax := min(int64(len(chunk)), bounds.InputBytes-state.result.InputBytes+1)
			n, readErr := src.Read(chunk[:int(readMax)])
			if n < 0 || n > int(readMax) {
				return state.result, errors.New("invalid stream reader count")
			}
			if n > 0 {
				empty = 0
				state.result.InputBytes += int64(n)
				if state.result.InputBytes > bounds.InputBytes {
					return state.result, &StreamLimitError{"screen_input_bytes", bounds.InputBytes, state.result.InputBytes}
				}
				if err := state.grow(len(state.buffer) + n); err != nil {
					return state.result, err
				}
				state.buffer = append(state.buffer, chunk[:n]...)
			} else {
				empty++
				if empty >= 100 && readErr == nil {
					return state.result, io.ErrNoProgress
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					return state.result, readErr
				}
				eof = true
			}
			continue
		}
		if err := state.advance(ctx, dst, window, overlap, longest, eof); err != nil {
			return state.result, err
		}
		if eof && state.emit == state.base+int64(len(state.buffer)) {
			if check != nil {
				err = check()
			}
			return state.result, err
		}
	}
}

type streamScreen struct {
	matcher                          *Matcher
	memory                           *streamMemory
	needles                          []streamNeedle
	hits                             streamHits
	bounds                           StreamBounds
	check                            func() error
	observe                          func(context.Context, StreamSpan) error
	buffer                           []byte
	bufferCharge                     int64
	base, catalog, emit, inputOrigin int64
	result                           StreamResult
}

func (s *streamScreen) grow(n int) error {
	if n <= cap(s.buffer) {
		return nil
	}
	capacity := max(n, 2*cap(s.buffer))
	charge := int64(capacity)
	if err := s.memory.take(charge); err != nil {
		return err
	}
	next := make([]byte, len(s.buffer), capacity)
	copy(next, s.buffer)
	s.buffer = next
	s.memory.release(s.bufferCharge)
	s.bufferCharge = charge
	return nil
}

func (s *streamScreen) advance(ctx context.Context, dst io.Writer, window, overlap, longest int, eof bool) error {
	// Reserve string copies, reference masking and bounded index capacities before
	// invoking the shared whole-input token mask over this retained segment.
	maskCharge := int64(len(s.buffer))*5 + int64(len(s.buffer)/referenceContextBytes+1)*256
	if err := s.memory.take(maskCharge); err != nil {
		return err
	}
	defer s.memory.release(maskCharge)
	raw := string(s.buffer)
	mask := maskReferenceTokens(raw)
	start := int(s.catalog - s.base)
	end := windowEnd(mask.text, start, window)
	if err := s.matcher.streamCatalog(ctx, mask.text[start:end], s.catalog, s.memory, &s.hits, s.bounds.DetectorWork); err != nil {
		return err
	}
	next := int64(end)
	if !eof || end < len(raw) {
		next = int64(windowNextStart(mask.text, start, end, overlap))
	}
	next += s.base
	harvestUntil := next
	if eof && end == len(raw) {
		harvestUntil = s.base + int64(len(raw))
	}
	if err := s.matcher.streamHarvest(ctx, mask.text, s.base, harvestUntil, eof, s.needles, &s.hits); err != nil {
		return err
	}
	s.catalog = next
	cutoff := harvestUntil
	// Do not split a pending covered interval. Later windows may extend it and the
	// existing union/attribution policy must see the complete interval before use.
	for changed := true; changed; {
		changed = false
		for _, hit := range s.hits.values {
			if hit.start < cutoff && hit.end > cutoff {
				cutoff = hit.start
				changed = true
			}
		}
	}
	if cutoff > s.emit {
		if err := s.flush(ctx, dst, raw, mask, cutoff); err != nil {
			return err
		}
	}
	// Retain left context for complete host reference tokens and word boundaries.
	retain := min(s.emit, s.catalog) - int64(referenceContextBytes+utf8.UTFMax)
	for _, n := range s.needles {
		retain = min(retain, n.cursor-int64(referenceContextBytes+utf8.UTFMax))
	}
	retain = max(s.base, retain)
	at := int(retain - s.base)
	at = originalRuneBoundary(raw, at)
	if at > 0 {
		copy(s.buffer, s.buffer[at:])
		s.buffer = s.buffer[:len(s.buffer)-at]
		s.base += int64(at)
	}
	return nil
}
