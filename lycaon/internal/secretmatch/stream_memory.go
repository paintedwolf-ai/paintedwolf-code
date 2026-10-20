package secretmatch

import (
	"fmt"
)

// StreamLimitError reports a complete-screen refusal; an earlier emitted prefix
// remains screened, but must never be treated as a successful complete artifact.
type StreamLimitError struct {
	Resource          string
	Maximum, Observed int64
}

func (e *StreamLimitError) Error() string {
	return fmt.Sprintf("%s: requested %d, maximum %d", e.Resource, e.Observed, e.Maximum)
}

type streamMemory struct{ maximum, used int64 }

func (m *streamMemory) take(n int64) error {
	if n < 0 || n > m.maximum-m.used {
		return &StreamLimitError{"screen_memory_bytes", m.maximum, m.used + n}
	}
	m.used += n
	return nil
}
func (m *streamMemory) release(n int64)  { m.used -= n }
func (m *streamMemory) available() int64 { return m.maximum - m.used }

// StreamSpan maps one written marker to the input bytes it removed. An observer
// must admit its own retained metadata before retaining this value.
type StreamSpan struct{ InputStart, InputEnd, OutputStart, OutputEnd int64 }

// StreamBounds are explicit operation budgets, separate from pipe queue capacity.
// Source-owned evidence and static catalog storage remain owned by their source.
type StreamBounds struct{ InputBytes, OutputBytes, MemoryBytes, DetectorWork int64 }

// StreamResult describes exact transferred bytes, including partial terminal work.
type StreamResult struct {
	InputBytes, OutputBytes int64
	Redacted                bool
	ClassificationRevision  string
}

type streamNeedle struct {
	needle      needle
	value       HarvestedValue
	cursor      int64
	occurrences int
}
type streamMatch struct {
	hit        Match
	start, end int64
}

// streamHits retains only metadata, never copies matched secret bytes.
type streamHits struct {
	values []streamMatch
	memory *streamMemory
	charge int64
}

func (h *streamHits) add(v streamMatch) error {
	if len(h.values) == cap(h.values) {
		capacity := max(8, 2*cap(h.values))
		charge := int64(capacity) * 512
		if err := h.memory.take(charge); err != nil {
			return err
		}
		next := make([]streamMatch, len(h.values), capacity)
		copy(next, h.values)
		h.values = next
		h.memory.release(h.charge)
		h.charge = charge
	}
	h.values = append(h.values, v)
	return nil
}
