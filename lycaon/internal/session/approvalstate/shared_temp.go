package approvalstate

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
)

// SharedTempHistory retains this chat's reports and serializes its approval requests.
type SharedTempHistory struct {
	review   chan struct{}
	mu       sync.Mutex
	restored bool
	reported map[string]bool
	Declined map[string]bool
}

type SharedTempHistoryReader func(context.Context, string, []string) (reported, declined []string, err error)

func (r *SandboxPathGrantRuntime) SharedTempHistory(ctx context.Context, chat string, restore SharedTempHistoryReader) (*SharedTempHistory, error) {
	entry, ok := r.tempChats.Load(chat)
	if !ok {
		entry, _ = r.tempChats.LoadOrStore(chat, &SharedTempHistory{review: make(chan struct{}, 1), reported: map[string]bool{}, Declined: map[string]bool{}})
	}
	history := entry.(*SharedTempHistory)
	history.mu.Lock()
	defer history.mu.Unlock()
	if !history.restored && restore != nil {
		reported, declined, err := restore(ctx, chat, confine.SharedTempLocations())
		if err != nil {
			return nil, err
		}
		for _, root := range reported {
			history.reported[root] = true
		}
		for _, root := range declined {
			history.Declined[root] = true
		}
	}
	history.restored = true
	return history, nil
}

func (h *SharedTempHistory) FirstReports(roots []string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var first []string
	for _, root := range roots {
		if !h.reported[root] {
			h.reported[root] = true
			first = append(first, root)
		}
	}
	return first
}

func (h *SharedTempHistory) BeginReview(ctx context.Context) (func(), error) {
	select {
	case h.review <- struct{}{}:
		return func() { <-h.review }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
