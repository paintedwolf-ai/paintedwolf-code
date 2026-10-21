package captureprojection

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// ValueStateAdmission reserves retained projection storage before allocation.
// The caller retains the original input separately until Value returns.
type ValueStateAdmission interface {
	ReserveProgramState(context.Context, int64) (func(), error)
}

// ValueOptions bounds the entire value traversal and its retained result.
// Stream byte limits are aggregate across keys, strings and numeric spellings.
// DetectorWork keeps ScreenStream's per-pass meaning; MaximumNodes bounds the
// number of scalar screening operations (object keys count as nodes too).
// Depth is zero-based at the root. WorkingMemory is shared with siblings.
type ValueOptions struct {
	State                      ValueStateAdmission
	Stream                     StreamOptions
	MaximumNodes, MaximumDepth int
}

// Value screens original scalar bytes before encoding. Its release must remain
// held through consumption/publication; an error returns no partial projection
// and releases all retained storage. It never mutates the input or reconstructs
// executable arguments from an inspection representation.
func (p *Projector) Value(ctx context.Context, scope Scope, label string, value any, options ValueOptions) (any, Metadata, func(), error) {
	if p == nil || p.matcher == nil || options.State == nil || options.Stream.WorkingMemory == nil {
		return nil, Metadata{}, nil, ErrUnavailable
	}
	b := options.Stream.Bounds
	if ctx == nil || options.MaximumNodes <= 0 || options.MaximumDepth <= 0 || b.InputBytes <= 0 || b.OutputBytes <= 0 || b.MemoryBytes <= 0 || b.DetectorWork <= 0 {
		return nil, Metadata{}, nil, ErrUnavailable
	}
	storage := &valueStorage{ctx: ctx, state: options.State}
	if _, err := storage.reserve(4096); err != nil {
		return nil, Metadata{}, nil, err
	}
	metadata := Metadata{StructuredCoverage: true}
	screener := valueScreen{projector: p, scope: scope, options: options, storage: storage, metadata: &metadata}
	walk := valueWalk{
		screen:    screener.text,
		admit:     func(_ context.Context, n int64) error { _, err := storage.reserve(n); return err },
		scalar:    screener.number,
		generated: func(n int64) error { return screener.count(0, n) },
	}
	nodes := 0
	walk.node = func(ctx context.Context, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		nodes++
		if nodes > options.MaximumNodes {
			return &secretmatch.StreamLimitError{Resource: "capture_value_nodes", Maximum: int64(options.MaximumNodes), Observed: int64(nodes)}
		}
		if depth > options.MaximumDepth {
			return &secretmatch.StreamLimitError{Resource: "capture_value_depth", Maximum: int64(options.MaximumDepth), Observed: int64(depth)}
		}
		_, err := storage.reserve(4096)
		return err
	}
	// A first and final empty stream also freeze/check classification for trees
	// without strings. Every intervening scalar must use that same generation.
	_, err := screener.text(label, "")
	var projected any
	if err == nil {
		projected, err = walk.walk(ctx, label, value, 0)
	}
	if err == nil {
		_, err = screener.text(label, "")
	}
	if err != nil {
		storage.close()
		return nil, Metadata{}, nil, err
	}
	return projected, metadata, storage.close, nil
}

type valueLease struct {
	release func()
	next    *valueLease
}
type valueStorage struct {
	ctx   context.Context
	state ValueStateAdmission
	head  *valueLease
	once  sync.Once
}

func (s *valueStorage) reserve(n int64) (func(), error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	// Include lease bookkeeping and conservative allocator/container overhead.
	release, err := s.state.ReserveProgramState(s.ctx, n+256)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, err
	}
	if release == nil {
		return nil, ErrUnavailable
	}
	var once sync.Once
	wrapped := func() { once.Do(release) }
	s.head = &valueLease{wrapped, s.head}
	return wrapped, nil
}
func (s *valueStorage) close() {
	s.once.Do(func() {
		for entry := s.head; entry != nil; entry = entry.next {
			entry.release()
		}
		s.head = nil
	})
}

type valueScreen struct {
	projector     *Projector
	scope         Scope
	options       ValueOptions
	storage       *valueStorage
	metadata      *Metadata
	input, output int64
	initial       *StreamMetadata
}

func (s *valueScreen) count(input, output int64) error {
	b := s.options.Stream.Bounds
	if input > b.InputBytes-s.input {
		return &secretmatch.StreamLimitError{Resource: "capture_value_input_bytes", Maximum: b.InputBytes, Observed: s.input + input}
	}
	if output > b.OutputBytes-s.output {
		return &secretmatch.StreamLimitError{Resource: "capture_value_output_bytes", Maximum: b.OutputBytes, Observed: s.output + output}
	}
	s.input += input
	s.output += output
	return nil
}
func (s *valueScreen) number(value string) (string, error) {
	if err := s.count(int64(len(value)), int64(len(value))); err != nil {
		return "", err
	}
	if _, err := s.storage.reserve(int64(len(value))*2 + 4096); err != nil {
		return "", err
	}
	if _, err := json.Marshal(json.Number(value)); err != nil {
		return "", err
	}
	return strings.Clone(value), nil
}
func (s *valueScreen) text(label, value string) (string, error) {
	if err := s.count(int64(len(value)), 0); err != nil {
		return "", err
	}
	options := s.options.Stream
	// ScreenBytes requires positive bounds even at an exactly consumed aggregate
	// limit. The writer below refuses any actual additional byte before growth.
	options.Bounds.InputBytes = max(1, int64(len(value)))
	options.Bounds.OutputBytes = max(1, s.options.Stream.Bounds.OutputBytes-s.output)
	observe := options.Observe
	options.Observe = func(ctx context.Context, span secretmatch.StreamSpan) error {
		s.metadata.RedactedCount++
		if observe != nil {
			return observe(ctx, span)
		}
		return nil
	}
	output := valueBuffer{storage: s.storage, maximum: s.options.Stream.Bounds.OutputBytes - s.output}
	metadata, err := s.projector.ScreenStream(s.storage.ctx, s.scope, label, strings.NewReader(value), &output, options)
	if err != nil {
		return "", err
	}
	if s.initial == nil {
		s.initial = &metadata
	} else if s.initial.ManagedGeneration != metadata.ManagedGeneration || s.initial.ClassificationRevision != metadata.ClassificationRevision || s.initial.CatalogVersion != metadata.CatalogVersion {
		return "", &secretmatch.StreamChangedError{Resource: "capture_value_classification"}
	}
	s.metadata.CatalogVersion = metadata.CatalogVersion
	s.output += metadata.EmittedBytes
	if _, err := s.storage.reserve(int64(len(output.bytes))*2 + 4096); err != nil {
		return "", err
	}
	result := string(output.bytes)
	output.bytes = nil
	if output.release != nil {
		output.release()
	}
	return result, nil
}

type valueBuffer struct {
	storage *valueStorage
	bytes   []byte
	maximum int64
	release func()
}

func (b *valueBuffer) Write(p []byte) (int, error) {
	needed := int64(len(b.bytes)) + int64(len(p))
	if needed > b.maximum {
		return 0, &secretmatch.StreamLimitError{Resource: "capture_value_output_bytes", Maximum: b.maximum, Observed: needed}
	}
	if needed > int64(cap(b.bytes)) {
		capacity := min(b.maximum, max(needed, int64(cap(b.bytes))*2))
		if capacity > int64(int(^uint(0)>>1)) {
			return 0, errors.New("capture value exceeds addressable storage")
		}
		release, err := b.storage.reserve(capacity + 4096)
		if err != nil {
			return 0, err
		}
		next := make([]byte, len(b.bytes), int(capacity))
		copy(next, b.bytes)
		previous := b.release
		b.bytes = next
		b.release = release
		if previous != nil {
			previous()
		}
	}
	b.bytes = append(b.bytes, p...)
	return len(p), nil
}
