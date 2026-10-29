package secretcap

import (
	"context"
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
	_, err := r.PrepareDelivery(ctx, included)
	return err
}

func (r *Resolution) PrepareDelivery(_ context.Context, included func(string) bool) (*PreparedDelivery, error) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	unreleased, held := r.heldWithoutRelease(included)
	r.mu.Unlock()
	if len(unreleased) > 0 {
		return nil, &HeldUnreleasedError{Names: unreleased}
	}
	delivery := &PreparedDelivery{resolution: r, included: included}
	if held {
		unlock, open := r.unlocks.Active(r.access.ChatSessionID)
		if !open {
			return nil, ErrVaultLocked
		}
		delivery.unlockID = unlock.ID
	}
	return delivery, nil
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
