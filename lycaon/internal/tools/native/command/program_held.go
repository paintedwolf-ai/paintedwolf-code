package command

import (
	"context"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// HeldEffect is one parked child: its frozen prepared invocation and the
// current-authority/revision recheck that must pass before its single attempt.
type HeldEffect struct {
	Identity toolcontract.ExecutionIdentity
	Recheck  func(context.Context) error
	Invoke   func(context.Context) (toolcontract.ProducerResult, error)
	Settle   func(context.Context, toolcontract.ProducerResult, string) error
}

// ProgramHeldEffects parks effects waiting on a person. Inputs and prepared
// bytes stay frozen; nothing upstream is recomputed. Each identity resumes at
// most once, and a stale recheck is a conflict rather than a refresh.
type ProgramHeldEffects struct {
	mu   sync.Mutex
	held map[string]*heldEntry
}

type heldEntry struct {
	effect  HeldEffect
	settled bool
}

var _ commandprogram.HeldEffects = (*ProgramHeldEffects)(nil)

// Park registers a held effect under its existing child identity.
func (h *ProgramHeldEffects) Park(effect HeldEffect) error {
	if effect.Identity.ExecutionID == "" || effect.Recheck == nil || effect.Invoke == nil || effect.Settle == nil {
		return fmt.Errorf("held effect requires identity, recheck, invocation and settlement")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held == nil {
		h.held = map[string]*heldEntry{}
	}
	if _, exists := h.held[effect.Identity.ExecutionID]; exists {
		return fmt.Errorf("execution %s is already held", effect.Identity.ExecutionID)
	}
	h.held[effect.Identity.ExecutionID] = &heldEntry{effect: effect}
	return nil
}

func (h *ProgramHeldEffects) take(identity toolcontract.ExecutionIdentity) (HeldEffect, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.held[identity.ExecutionID]
	if !ok || entry.effect.Identity != identity {
		return HeldEffect{}, toolrejection.RejectInvalidArguments("PROCESS_REFERENCE_STALE", map[string]any{"execution_id": identity.ExecutionID, "reason": "no held effect with this identity"})
	}
	if entry.settled {
		return HeldEffect{}, toolrejection.RejectInvalidArguments("PROCESS_REFERENCE_STALE", map[string]any{"execution_id": identity.ExecutionID, "reason": "held effect already settled"})
	}
	entry.settled = true
	return entry.effect, nil
}

// Resume runs the exact held effect once after a fresh recheck. A failed
// recheck settles the effect as a conflict; the caller ends the program.
func (h *ProgramHeldEffects) Resume(ctx context.Context, identity toolcontract.ExecutionIdentity) (toolcontract.ProducerResult, error) {
	effect, err := h.take(identity)
	if err != nil {
		return toolcontract.ProducerResult{}, err
	}
	if err := effect.Recheck(ctx); err != nil {
		conflict := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: "EDIT_TARGET_CHANGED_BY_OTHERS", EffectCertainty: toolcontract.NoEffect}
		return conflict, joinSettle(err, effect.Settle(ctx, conflict, "EDIT_TARGET_CHANGED_BY_OTHERS"))
	}
	result, err := effect.Invoke(ctx)
	return result, joinSettle(err, effect.Settle(ctx, result, ""))
}

// Stop settles a held effect as denied without invoking it.
func (h *ProgramHeldEffects) Stop(ctx context.Context, identity toolcontract.ExecutionIdentity) error {
	effect, err := h.take(identity)
	if err != nil {
		return err
	}
	denied := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: approvaloutcome.CodeApprovalDenied, EffectCertainty: toolcontract.NoEffect}
	return effect.Settle(ctx, denied, approvaloutcome.CodeApprovalDenied)
}

func joinSettle(err, settle error) error {
	if settle == nil {
		return err
	}
	if err == nil {
		return settle
	}
	return fmt.Errorf("%w; settlement: %v", err, settle)
}
