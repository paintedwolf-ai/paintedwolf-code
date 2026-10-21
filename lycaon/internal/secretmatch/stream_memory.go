package secretmatch

import (
	"context"
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

// StreamMemoryAdmission charges actual reserved payload before allocation. It
// must refuse unavailable aggregate capacity without waiting on this program.
// ReleaseBytes returns reservations only after their payload is unreachable.
type StreamMemoryAdmission interface {
	ReserveBytes(context.Context, int64) error
	ReleaseBytes(int64)
}

type streamMemory struct {
	maximum, used int64
	ctx           context.Context
	admission     StreamMemoryAdmission
}

func (m *streamMemory) take(n int64) error {
	if n < 0 || n > m.maximum-m.used {
		return &StreamLimitError{"screen_memory_bytes", m.maximum, m.used + n}
	}
	if m.admission != nil && n > 0 {
		if err := m.admission.ReserveBytes(m.ctx, n); err != nil {
			return err
		}
	}
	m.used += n
	return nil
}
func (m *streamMemory) release(n int64) {
	if n < 0 || n > m.used {
		panic("invalid stream memory release")
	}
	m.used -= n
	if m.admission != nil && n > 0 {
		m.admission.ReleaseBytes(n)
	}
}
func (m *streamMemory) available() int64 { return m.maximum - m.used }

// StreamSpan maps one written marker to the input bytes it removed. An observer
// must admit its own retained metadata before retaining this value.
type StreamSpan struct{ InputStart, InputEnd, OutputStart, OutputEnd int64 }

// StreamBounds are explicit operation budgets, separate from pipe queue capacity.
// DetectorWork bounds each catalog pass and each pending evidence-fusion step.
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
