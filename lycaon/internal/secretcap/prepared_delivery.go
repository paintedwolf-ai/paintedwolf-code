package secretcap

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"
)

// PreparedDelivery binds an effect to the unlock checked at its commit boundary.
type PreparedDelivery struct {
	once       sync.Once
	resolution *Resolution
	included   func(string) bool
	unlockID   string
}

// Precheck verifies release and unlock authority without recording a use.
func (r *Resolution) Precheck(ctx context.Context, included func(string) bool) error {
	_, err := r.prepareDelivery(ctx, included, false)
	return err
}

func (r *Resolution) PrepareDelivery(ctx context.Context, included func(string) bool) (*PreparedDelivery, error) {
	return r.prepareDelivery(ctx, included, true)
}

func (r *Resolution) prepareDelivery(ctx context.Context, included func(string) bool, recordRefusal bool) (*PreparedDelivery, error) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	selected := r.selectedIDs(included)
	unreleased, held := r.heldWithoutRelease(included)
	var activeIDs []string
	for id := range selected {
		if !r.delivery[id].redacted {
			activeIDs = append(activeIDs, id)
		}
	}
	r.mu.Unlock()
	if len(unreleased) > 0 {
		if recordRefusal {
			r.recordDelivery(ctx, DeliveryWithheld, included, "")
		}
		return nil, &HeldUnreleasedError{Names: unreleased}
	}
	if r.service != nil && len(activeIDs) > 0 {
		sort.Strings(activeIDs)
		for _, id := range activeIDs {
			if err := r.service.checkCapabilityActive(ctx, id, r.access); err != nil {
				if recordRefusal {
					r.recordDelivery(ctx, DeliveryWithheld, included, "")
				}
				return nil, err
			}
		}
	}
	delivery := &PreparedDelivery{resolution: r, included: included}
	if held {
		unlock, open := r.unlocks.Active(r.access.ChatSessionID)
		if !open {
			if recordRefusal {
				r.recordDelivery(ctx, DeliveryWithheld, included, "")
			}
			return nil, ErrVaultLocked
		}
		delivery.unlockID = unlock.ID
	}
	return delivery, nil
}

func (s *Service) checkCapabilityActive(ctx context.Context, id string, access ResolveContext) error {
	if s == nil || s.queries == nil {
		return nil
	}
	row, err := s.queries.GetManagedSecret(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := visible(row, access.ProjectID, access.ChatSessionID); err != nil {
		return err
	}
	if row.RevokedAt.Valid {
		return ErrRevoked
	}
	if agentUseEnded(row.AgentUseEndsAt, s.now()) {
		return ErrAgentUseExpired
	}
	return nil
}

// Commit records only an effect that reached its recipient.
func (d *PreparedDelivery) Commit(ctx context.Context) {
	if d == nil {
		return
	}
	d.once.Do(func() {
		if d.unlockID != "" {
			d.resolution.unlocks.UseIfCurrent(d.resolution.access.ChatSessionID, d.unlockID)
		}
		d.resolution.recordDelivery(ctx, DeliveryHandedOff, d.included, d.unlockID)

	})
}
