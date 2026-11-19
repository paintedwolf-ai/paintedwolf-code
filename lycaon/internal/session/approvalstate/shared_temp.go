package approvalstate

import (
	"context"
)

// SharedTempHistory serializes recovery for the machine's finite set of temp locations.
type SharedTempHistory struct {
	review   chan struct{}
	Reported map[string]bool
	Declined map[string]bool
}

func (r *SandboxPathGrantRuntime) BeginSharedTemp(ctx context.Context, chat string) (*SharedTempHistory, func(), error) {
	entry, _ := r.tempChats.LoadOrStore(chat, &SharedTempHistory{review: make(chan struct{}, 1), Reported: map[string]bool{}, Declined: map[string]bool{}})
	history := entry.(*SharedTempHistory)
	select {
	case history.review <- struct{}{}:
		return history, func() { <-history.review }, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}
