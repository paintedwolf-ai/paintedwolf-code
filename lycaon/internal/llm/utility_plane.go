package llm

import (
	"context"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// UtilityPlane owns lite health, shared call admission, and utility budgets.
type UtilityPlane struct {
	health *slotHealth
	lanes  *utilityLanes
}

// NewUtilityPlane constructs a shared plane.
func NewUtilityPlane() *UtilityPlane {
	return &UtilityPlane{
		health: newSlotHealth(),
		lanes:  newUtilityLanes(),
	}
}

// SetOnChange is notified when the slot moves between ready and unavailable.
// App wires this to the lite_slot preflight probe.
func (p *UtilityPlane) SetOnChange(fn func(SlotSnapshot)) {
	if p == nil {
		return
	}
	p.health.setOnChange(fn)
}

// Snapshot is the current lite-slot observation.
func (p *UtilityPlane) Snapshot() SlotSnapshot {
	if p == nil {
		return SlotSnapshot{State: SlotReady}
	}
	return p.health.snapshot()
}

// Reset clears a down observation.
func (p *UtilityPlane) Reset() {
	if p == nil {
		return
	}
	p.health.reset()
}

func (p *UtilityPlane) allowLite() bool {
	if p == nil {
		return true
	}
	return p.health.allow()
}

func (p *UtilityPlane) noteOutcome(providerID string, err error) {
	if p == nil {
		return
	}
	if err == nil {
		p.health.success()
		return
	}
	reason, down := liteSlotDown(err)
	if !down {
		return
	}
	p.health.failure(providerID, reason)
}

// noteCallOutcome updates owned slot health and pair refusals.
func (r *RegistrySummarizer) noteCallOutcome(observe bool, providerID, model string, err error) {
	if r == nil {
		return
	}
	if err == nil {
		r.Refusals.Clear(providerID, model)
	}
	r.Refusals.Note(providerID, model, err)
	if observe && r.Plane != nil {
		if r.Purpose == "compaction" {
			r.Plane.noteCompactionOutcome(providerID, err)
		} else {
			r.Plane.noteOutcome(providerID, err)
		}
	}
}

func (p *UtilityPlane) acquireLane(ctx context.Context, provider modelcall.Provider, class UtilityClass) (func(), error) {
	if p == nil || provider == nil {
		return func() {}, nil
	}
	return p.lanes.acquire(ctx, provider.ID(), provider.Profile().UtilitySingleFlight, class)
}

func (p *UtilityPlane) acquireForeground(ctx context.Context, provider modelcall.Provider) (func(), error) {
	if p == nil || provider == nil {
		return func() {}, nil
	}
	return p.lanes.acquireForeground(ctx, provider.ID(), provider.Profile().UtilitySingleFlight)
}

// Residency removes load time, not prompt evaluation or generation.
func resolveUtilityAttemptTimeout(p modelcall.Provider, class UtilityClass) time.Duration {
	profile := providerprofile.Default()
	if p != nil {
		profile = p.Profile()
	}
	return resolveUtilityCallTimeout(profile, class)
}

func skipLiteError(err error) bool {
	return errors.Is(err, ErrLiteUnavailable) || errors.Is(err, ErrLiteBusy)
}

// noteCompactionOutcome reports sustained endpoint failures, excluding latency.
func (p *UtilityPlane) noteCompactionOutcome(providerID string, err error) {
	if err == nil {
		p.health.success()
		return
	}
	reason, down := liteSlotDown(err)
	if down && reason != "silent" {
		p.health.failureAfter(providerID, reason, 3)
	}
}
