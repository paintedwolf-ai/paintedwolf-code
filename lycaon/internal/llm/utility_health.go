package llm

import (
	"errors"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

// SlotState is the observed liveness of the lite assignment. It is not
// eligibility: ready_to_assign stays a config+discovery fact.
type SlotState string

const (
	SlotReady       SlotState = "ready"
	SlotUnavailable SlotState = "unavailable"
)

// LiteUnavailableCooldown is how long a failed lite slot stays skipped
// before one more attempt is admitted.
const LiteUnavailableCooldown = 2 * time.Minute

// ErrLiteUnavailable means the lite slot is known down; callers take their
// class fallback immediately and do not wait out a local-inference budget.
var ErrLiteUnavailable = errors.New("lite slot unavailable")

// ErrLiteBusy means a single-flight instance is already occupied. Overlay skips.
var ErrLiteBusy = errors.New("lite slot busy")

// SlotSnapshot is the last observed lite-slot fact. Reason is one of refused,
// overloaded, silent, or unreachable.
type SlotSnapshot struct {
	State      SlotState
	ProviderID string
	Reason     string
	Since      time.Time
}

type slotHealth struct {
	mu       sync.Mutex
	snap     SlotSnapshot
	onChange func(SlotSnapshot)
}

func newSlotHealth() *slotHealth {
	return &slotHealth{snap: SlotSnapshot{State: SlotReady}}
}

func (h *slotHealth) setOnChange(fn func(SlotSnapshot)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.onChange = fn
	h.mu.Unlock()
}

func (h *slotHealth) snapshot() SlotSnapshot {
	if h == nil {
		return SlotSnapshot{State: SlotReady}
	}
	h.mu.Lock()
	expired := h.maybeExpireLocked()
	snap := h.snap
	fn := h.onChange
	h.mu.Unlock()
	if expired && fn != nil {
		fn(SlotSnapshot{State: SlotReady})
	}
	return snap
}

func (h *slotHealth) allow() bool {
	return h.snapshot().State != SlotUnavailable
}

func (h *slotHealth) success() {
	if h == nil {
		return
	}
	h.mu.Lock()
	prev := h.snap.State
	h.snap = SlotSnapshot{State: SlotReady, Since: time.Now().UTC()}
	fn := h.onChange
	h.mu.Unlock()
	if prev == SlotUnavailable && fn != nil {
		fn(SlotSnapshot{State: SlotReady})
	}
}

func (h *slotHealth) failure(providerID, reason string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	prev := h.snap.State
	h.snap = SlotSnapshot{
		State:      SlotUnavailable,
		ProviderID: providerID,
		Reason:     reason,
		Since:      time.Now().UTC(),
	}
	fn := h.onChange
	snap := h.snap
	h.mu.Unlock()
	if prev != SlotUnavailable && fn != nil {
		fn(snap)
	}
}

func (h *slotHealth) reset() {
	if h == nil {
		return
	}
	h.mu.Lock()
	prev := h.snap.State
	h.snap = SlotSnapshot{State: SlotReady, Since: time.Now().UTC()}
	fn := h.onChange
	h.mu.Unlock()
	if prev == SlotUnavailable && fn != nil {
		fn(SlotSnapshot{State: SlotReady})
	}
}

func (h *slotHealth) maybeExpireLocked() bool {
	if h.snap.State != SlotUnavailable {
		return false
	}
	if h.snap.Since.IsZero() || time.Since(h.snap.Since) < LiteUnavailableCooldown {
		return false
	}
	h.snap = SlotSnapshot{State: SlotReady, Since: time.Now().UTC()}
	return true
}

// liteSlotDown names why a failed attempt means the lite slot cannot serve. It
// reads the typed transport faults the capacity gate cools on; caller
// cancellation, the utility budget, and bad answers never produce them.
func liteSlotDown(err error) (reason string, down bool) {
	if err == nil {
		return "", false
	}
	// A model refusal retires the assigned slot.
	if _, ok := providerretry.AsModelRefused(err); ok {
		return "refused", true
	}
	kind, ok := slotUnavailable(err)
	if !ok {
		return "", false
	}
	switch kind {
	case providerretry.FaultCapacity:
		return "overloaded", true
	case providerretry.FaultSilent:
		return "silent", true
	default:
		return "unreachable", true
	}
}
