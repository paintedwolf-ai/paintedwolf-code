package secretcap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
func (r *Resolution) Withhold(ctx context.Context) { r.recordDelivery(ctx, DeliveryWithheld, nil, "") }

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
// the vault's way out: a person-held value leaves only under a reviewed
// release while its chat is unlocked, and each use extends the unlock.
func (r *Resolution) HandOff(ctx context.Context, included func(string) bool) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	unreleased, held := r.heldWithoutRelease(included)
	r.mu.Unlock()
	if len(unreleased) > 0 {
		r.recordDelivery(ctx, DeliveryWithheld, included, "")
		return &HeldUnreleasedError{Names: unreleased}
	}
	unlockID := ""
	if held {
		unlock, open := r.unlocks.Use(r.access.ChatSessionID)
		if !open {
			r.recordDelivery(ctx, DeliveryWithheld, included, "")
			return ErrVaultLocked
		}
		unlockID = unlock.ID
	}
	r.recordDelivery(ctx, DeliveryHandedOff, included, unlockID)
	return nil
}

// ErrVaultLocked: a person-held value was about to leave while its chat was locked.
var ErrVaultLocked = errors.New("values a person holds are locked for this chat")

// HeldUnreleasedError names person-held values a consumer was about to
// receive without a reviewed release.
type HeldUnreleasedError struct{ Names []string }

func (e *HeldUnreleasedError) Error() string {
	return "a value a person holds has no reviewed release: " + strings.Join(e.Names, ", ")
}

// Finish settles attempts that never reached a consumer, including canceled calls.
func (r *Resolution) Finish(ctx context.Context) {
	r.recordDelivery(ctx, DeliveryNotDispatched, nil, "")
}

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

// recordDelivery settles each selected use once; a handoff records its
// reviewed recipients and the unlock it used.
func (r *Resolution) recordDelivery(ctx context.Context, delivery string, included func(string) bool, unlockID string) {
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
			recipients, _ := r.releasedToLocked(v.fingerprint)
			params.RecipientsJson = useRecipientsJSON(recipients)
			if v.custody.Held() {
				params.UnlockID = sql.NullString{String: unlockID, Valid: unlockID != ""}
			}
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
