package secretcap

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/secretmatch"
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

// UseRecipient is one reviewed recipient as use history records it.
type UseRecipient struct {
	Label   string `json:"label"`
	Surface string `json:"surface"`
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
// Every consumer calls it immediately before its transport, which makes it
// the vault's way out: it refuses while a person-held value lacks an attested
// release, so the value never reaches a recipient the person did not approve.
func (r *Resolution) HandOff(ctx context.Context, included func(string) bool) error {
	if names := r.UnreleasedHeld(included); len(names) > 0 {
		r.recordDelivery(ctx, DeliveryWithheld, included)
		return &HeldUnreleasedError{Names: names}
	}
	r.recordDelivery(ctx, DeliveryHandedOff, included)
	return nil
}

// HeldUnreleasedError names person-held values a consumer was about to
// receive without their attested release.
type HeldUnreleasedError struct{ Names []string }

func (e *HeldUnreleasedError) Error() string {
	return "a value a person holds has no attested release: " + strings.Join(e.Names, ", ")
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
		params := db.UpdateManagedSecretDeliveryParams{Delivery: state.outcome, RecipientsJson: "[]", ID: v.useID}
		if state.outcome == DeliveryHandedOff {
			recipients, attestation := r.releaseOfLocked(v.fingerprint)
			params.RecipientsJson = useRecipientsJSON(recipients)
			params.AttestationID = sql.NullString{String: attestation, Valid: attestation != ""}
		}
		if err := r.service.queries.UpdateManagedSecretDelivery(ctx, params); err != nil {
			slog.WarnContext(ctx, "managed secret delivery history write failed", "secret_id", v.id, "tool_call_id", r.access.ToolCallID)
		}
	}
}

// decodeUseRecipients reads a stored recipient list; the column's CHECK
// admits only arrays, so a decode failure names no recipient.
func decodeUseRecipients(raw string) []UseRecipient {
	out := []UseRecipient{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []UseRecipient{}
	}
	return out
}

func useRecipientsJSON(recipients []secretmatch.Recipient) string {
	out := make([]UseRecipient, 0, len(recipients))
	seen := map[UseRecipient]bool{}
	for _, recipient := range recipients {
		entry := UseRecipient{Label: recipient.Label, Surface: string(recipient.Surface)}
		if !seen[entry] {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}
