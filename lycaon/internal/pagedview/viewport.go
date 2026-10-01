package pagedview

import (
	"context"
	"sync"
	"time"
)

const ViewportLifetime = 30 * time.Second

// ViewportPages orders visible pages before symmetric nearby pages.
func ViewportPages(start, end, total int64, direction int) []int64 {
	if start < 0 || end <= start || start >= total {
		return nil
	}
	first, last := start/MaxRows, (min(end, total)-1)/MaxRows
	var pages []int64
	for at := first; at <= last; at++ {
		pages = append(pages, at*MaxRows)
	}
	for distance := int64(1); distance <= 3; distance++ {
		near := [2]int64{last + distance, first - distance}
		if direction < 0 {
			near[0], near[1] = near[1], near[0]
		}
		for _, at := range near {
			if at >= 0 && at*MaxRows < total {
				pages = append(pages, at*MaxRows)
			}
		}
	}
	return pages
}

type viewportInterest struct {
	sequence int64
	until    time.Time
	cancel   context.CancelFunc
}

// ViewportInterests bounds each view's replaceable preparation consumers.
type ViewportInterests struct {
	mu      sync.Mutex
	entries map[string]viewportInterest
	closed  bool
	workers sync.WaitGroup
}

func (v *ViewportInterests) Replace(ctx context.Context, id string, sequence int64, work func(context.Context), release func()) error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		release()
		return ErrExpired
	}
	if v.entries == nil {
		v.entries = make(map[string]viewportInterest)
	}
	now := time.Now()
	for key, entry := range v.entries {
		if !now.Before(entry.until) {
			entry.cancel()
			delete(v.entries, key)
		}
	}
	previous, exists := v.entries[id]
	if exists && sequence <= previous.sequence {
		v.mu.Unlock()
		release()
		return nil
	}
	if !exists && len(v.entries) >= 8 {
		v.mu.Unlock()
		release()
		return ErrBudget
	}
	if exists {
		previous.cancel()
	}
	lifetime, cancel := context.WithTimeout(ctx, ViewportLifetime)
	v.entries[id] = viewportInterest{sequence: sequence, until: now.Add(ViewportLifetime), cancel: cancel}
	v.workers.Add(1)
	v.mu.Unlock()
	go func() { defer release(); defer v.workers.Done(); defer cancel(); work(lifetime) }()
	return nil
}

func (v *ViewportInterests) Release(id string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if entry, ok := v.entries[id]; ok {
		entry.cancel()
		delete(v.entries, id)
	}
}

func (v *ViewportInterests) Close() {
	v.mu.Lock()
	v.closed = true
	for _, entry := range v.entries {
		entry.cancel()
	}
	v.entries = nil
	v.mu.Unlock()
	v.workers.Wait()
}
