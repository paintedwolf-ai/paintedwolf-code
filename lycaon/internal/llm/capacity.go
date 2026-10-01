package llm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

// DefaultCapacityPolicy is the ship hold-and-cooldown schedule.
func DefaultCapacityPolicy() CapacityPolicy {
	return CapacityPolicy{
		HoldMax:       2,
		HoldWaitMs:    []int{20000, 20000},
		HoldMaxWaitMs: 45000,
		CooldownMs:    30000,
		SilentMs:      60000,
	}
}

// CapacityPolicy is the turn-level hold and provider-slot cooldown.
type CapacityPolicy struct {
	HoldMax       int   `yaml:"max_holds"`
	HoldWaitMs    []int `yaml:"wait_ms"`
	HoldMaxWaitMs int   `yaml:"max_wait_ms"`
	CooldownMs    int   `yaml:"overloaded_ms"`
	// SilentMs cools a slot whose provider took a request and never answered.
	// It outlasts the overload cooldown because silence costs a full header bound.
	SilentMs int `yaml:"silent_ms"`
}

// CapacityHoldConfig is the turn-level hold schedule.
type CapacityHoldConfig struct {
	MaxHolds  int   `yaml:"max_holds"`
	WaitMs    []int `yaml:"wait_ms"`
	MaxWaitMs int   `yaml:"max_wait_ms"`
}

// CapacityCooldownConfig is the per-slot cooldown after a spent overload.
type CapacityCooldownConfig struct {
	OverloadedMs int `yaml:"overloaded_ms"`
	// SilentMs cools a slot after a provider went quiet. Absent means the
	// overload cooldown is reused.
	SilentMs int `yaml:"silent_ms,omitempty"`
}

// ProviderCapacityConfig is the providers.yaml root block.
type ProviderCapacityConfig struct {
	Hold     CapacityHoldConfig     `yaml:"hold"`
	Cooldown CapacityCooldownConfig `yaml:"cooldown"`
}

// IsZero reports an absent catalog block.
func (c ProviderCapacityConfig) IsZero() bool {
	return c.Hold.MaxHolds == 0 && c.Hold.MaxWaitMs == 0 && len(c.Hold.WaitMs) == 0 && c.Cooldown.OverloadedMs == 0
}

// Policy resolves a catalog block. An absent block is the ship default.
func (c ProviderCapacityConfig) Policy() (CapacityPolicy, error) {
	if c.IsZero() {
		return DefaultCapacityPolicy(), nil
	}
	silent := c.Cooldown.SilentMs
	if silent == 0 {
		silent = c.Cooldown.OverloadedMs
	}
	p := CapacityPolicy{
		HoldMax:       c.Hold.MaxHolds,
		HoldWaitMs:    append([]int(nil), c.Hold.WaitMs...),
		HoldMaxWaitMs: c.Hold.MaxWaitMs,
		CooldownMs:    c.Cooldown.OverloadedMs,
		SilentMs:      silent,
	}
	if err := ValidateCapacityPolicy(p); err != nil {
		return CapacityPolicy{}, err
	}
	return p, nil
}

// ValidateCapacityPolicy checks a resolved hold-and-cooldown schedule.
func ValidateCapacityPolicy(p CapacityPolicy) error {
	if p.HoldMax < 0 {
		return fmt.Errorf("provider_capacity.hold.max_holds must be >= 0")
	}
	if p.CooldownMs < 0 {
		return fmt.Errorf("provider_capacity.cooldown.overloaded_ms must be >= 0")
	}
	if p.SilentMs < 0 {
		return fmt.Errorf("provider_capacity.cooldown.silent_ms must be >= 0")
	}
	if p.HoldMax == 0 {
		return nil
	}
	if p.HoldMaxWaitMs <= 0 {
		return fmt.Errorf("provider_capacity.hold.max_wait_ms must be > 0 when max_holds > 0")
	}
	if len(p.HoldWaitMs) < p.HoldMax {
		return fmt.Errorf("provider_capacity.hold.wait_ms length %d < max_holds %d", len(p.HoldWaitMs), p.HoldMax)
	}
	for i, ms := range p.HoldWaitMs {
		if ms <= 0 {
			return fmt.Errorf("provider_capacity.hold.wait_ms[%d] must be > 0", i)
		}
	}
	return nil
}

// CapacityGate is the process-wide coordinator/worker capacity plane.
type CapacityGate struct {
	mu     sync.Mutex
	policy CapacityPolicy
	slots  map[string]time.Time
	now    func() time.Time
}

func NewCapacityGate(policy CapacityPolicy) *CapacityGate {
	return &CapacityGate{
		policy: policy,
		slots:  make(map[string]time.Time),
		now:    time.Now,
	}
}

func (g *CapacityGate) SetPolicy(policy CapacityPolicy) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.policy = policy
	g.mu.Unlock()
}

// Reset clears every cooling slot.
func (g *CapacityGate) Reset() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.slots = make(map[string]time.Time)
	g.mu.Unlock()
}

func capacitySlotKey(providerID, model string) string {
	return providerID + "\x00" + model
}

func (g *CapacityGate) remainingLocked(providerID, model string) time.Duration {
	until, ok := g.slots[capacitySlotKey(providerID, model)]
	if !ok {
		return 0
	}
	left := until.Sub(g.now())
	if left <= 0 {
		delete(g.slots, capacitySlotKey(providerID, model))
		return 0
	}
	return left
}

// Await sleeps the remaining cooldown. Cancel leaves the slot in place.
func (g *CapacityGate) Await(ctx context.Context, providerID, model string) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	left := g.remainingLocked(providerID, model)
	g.mu.Unlock()
	if left <= 0 {
		return nil
	}
	providerretry.ObserveRetry(ctx, providerretry.RetryAttempt{
		Attempt:     1,
		MaxAttempts: 1,
		Wait:        left,
		Status:      503,
		Reason:      providerretry.RetryReasonCooldown,
	})
	return providerretry.SleepHTTPRetry(ctx, left)
}

// NoteSuccess clears the slot after a 2xx stream opens.
func (g *CapacityGate) NoteSuccess(providerID, model string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.slots, capacitySlotKey(providerID, model))
	g.mu.Unlock()
}

// slotUnavailable reports an error meaning the provider slot cannot serve right
// now, as opposed to one meaning this request was wrong.
func slotUnavailable(err error) (providerretry.FaultKind, bool) {
	if _, ok := failure.AsProviderOverloaded(err); ok {
		return providerretry.FaultCapacity, true
	}
	if _, ok := failure.AsProviderSilent(err); ok {
		return providerretry.FaultSilent, true
	}
	if _, ok := failure.AsProviderUnreachable(err); ok {
		return providerretry.FaultUnreachable, true
	}
	return providerretry.FaultNone, false
}

// NoteFailure opens the cooldown for a slot that could not serve. Other errors
// are ignored: a rejected request says nothing about the provider's health.
func (g *CapacityGate) NoteFailure(providerID, model string, err error) {
	if g == nil {
		return
	}
	kind, ok := slotUnavailable(err)
	if !ok {
		return
	}
	g.mu.Lock()
	cool := g.cooldownForLocked(kind)
	if cool > 0 {
		g.slots[capacitySlotKey(providerID, model)] = g.now().Add(cool)
	}
	g.mu.Unlock()
}

// cooldownForLocked picks the schedule for the fault that closed the slot.
func (g *CapacityGate) cooldownForLocked(kind providerretry.FaultKind) time.Duration {
	if kind == providerretry.FaultSilent && g.policy.SilentMs > 0 {
		return time.Duration(g.policy.SilentMs) * time.Millisecond
	}
	return time.Duration(g.policy.CooldownMs) * time.Millisecond
}

// ShouldHold reports whether this error may take another turn-level hold.
func (g *CapacityGate) ShouldHold(err error, holdIndex int) bool {
	if g == nil {
		return false
	}
	if _, ok := slotUnavailable(err); !ok {
		return false
	}
	g.mu.Lock()
	max := g.policy.HoldMax
	g.mu.Unlock()
	return holdIndex >= 0 && holdIndex < max
}

// Hold sleeps one turn-level wait before the same iteration is reopened.
func (g *CapacityGate) Hold(ctx context.Context, holdIndex int) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	wait := holdWaitLocked(g.policy, holdIndex)
	maxHolds := g.policy.HoldMax
	g.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	providerretry.ObserveRetry(ctx, providerretry.RetryAttempt{
		Attempt:     holdIndex + 1,
		MaxAttempts: maxHolds,
		Wait:        wait,
		Status:      503,
		Reason:      providerretry.RetryReasonHold,
	})
	return providerretry.SleepHTTPRetry(ctx, wait)
}

func holdWaitLocked(p CapacityPolicy, holdIndex int) time.Duration {
	if holdIndex < 0 || holdIndex >= p.HoldMax || holdIndex >= len(p.HoldWaitMs) {
		return 0
	}
	d := time.Duration(p.HoldWaitMs[holdIndex]) * time.Millisecond
	return providerretry.ClampWait(d, time.Duration(p.HoldMaxWaitMs)*time.Millisecond)
}
