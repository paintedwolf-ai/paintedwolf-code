package secretcap

import (
	"context"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

const (
	DeliveryPending       = "pending"
	DeliveryNotDispatched = "not_dispatched"
	DeliveryWithheld      = "withheld"
	DeliveryHandedOff     = "handed_off"
	DeliveryRedacted      = "redacted"
)

type deliveryState struct {
	outcome  string
	redacted bool
}

// Withhold records a policy refusal before handoff.
func (r *Resolution) Withhold(ctx context.Context) { r.recordDelivery(ctx, DeliveryWithheld, nil) }

// Redacted marks only identities consumed by the rewritten transport fields.
func (r *Resolution) Redacted(included func(string) bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.delivery == nil {
		r.delivery = map[string]deliveryState{}
	}
	for id := range r.selectedIDs(included) {
		state := r.delivery[id]
		state.redacted = true
		r.delivery[id] = state
	}
}

// HandOff marks delivery to an executor or transport, not remote success.
func (r *Resolution) HandOff(ctx context.Context, included func(string) bool) {
	r.recordDelivery(ctx, DeliveryHandedOff, included)
}

// Finish settles attempts that never reached a consumer, including canceled calls.
func (r *Resolution) Finish(ctx context.Context) { r.recordDelivery(ctx, DeliveryNotDispatched, nil) }

func (r *Resolution) selectedIDs(included func(string) bool) map[string]bool {
	ids := map[string]bool{}
	if included == nil {
		for id := range r.values {
			ids[id] = true
		}
	} else {
		for _, b := range r.bindings {
			if included(b.path) {
				ids[b.id] = true
			}
		}
	}
	return ids
}

func (r *Resolution) recordDelivery(ctx context.Context, delivery string, included func(string) bool) {
	if r == nil || r.service == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.delivery == nil {
		r.delivery = map[string]deliveryState{}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	for id := range r.selectedIDs(included) {
		state := r.delivery[id]
		if state.outcome != "" {
			continue
		}
		state.outcome = delivery
		if delivery == DeliveryHandedOff && state.redacted {
			state.outcome = DeliveryRedacted
		}
		r.delivery[id] = state
		v := r.values[id]
		if v.useID == "" {
			continue
		}
		if err := r.service.queries.UpdateManagedSecretDelivery(ctx, db.UpdateManagedSecretDeliveryParams{Delivery: state.outcome, ID: v.useID}); err != nil {
			slog.WarnContext(ctx, "managed secret delivery history write failed", "secret_id", v.id, "tool_call_id", r.access.ToolCallID)
		}
	}
}
