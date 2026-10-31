package textutil

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// xargsHandoff joins the bounded parser queue, child completions and evaluator
// activity facts. Queued input and completed children make the dispatcher runnable.
type xargsHandoff struct {
	mu                               sync.Mutex
	activity                         *commandprogram.ExecutionWait
	next                             chan xargsNext
	space                            chan struct{}
	closed                           bool
	waitingInput                     bool
	waitingChild                     <-chan struct{}
	dispatcherUnblock, parserUnblock func()
}

func newXargsHandoff(activity *commandprogram.ExecutionWait) *xargsHandoff {
	return &xargsHandoff{activity: activity, next: make(chan xargsNext, 1), space: make(chan struct{}, 1)}
}
func clearXargsWait(unblock *func()) {
	if *unblock != nil {
		(*unblock)()
		*unblock = nil
	}
}
func (h *xargsHandoff) publish(ctx context.Context, activity *commandprogram.ExecutionWait, item xargsNext) error {
	for {
		h.mu.Lock()
		select {
		case h.next <- item:
			if h.waitingInput {
				clearXargsWait(&h.dispatcherUnblock)
			}
			clearXargsWait(&h.parserUnblock)
			h.mu.Unlock()
			return nil
		default:
		}
		var err error
		h.parserUnblock, err = activity.Block(ctx, commandprogram.WaitState{})
		h.mu.Unlock()
		if err != nil {
			return err
		}
		select {
		case <-h.space:
		case <-ctx.Done():
			return ctx.Err()
		}
		h.mu.Lock()
		clearXargsWait(&h.parserUnblock)
		h.mu.Unlock()
	}
}
func (h *xargsHandoff) parsed() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.waitingInput {
		clearXargsWait(&h.dispatcherUnblock)
	}
	clearXargsWait(&h.parserUnblock)
	h.closed = true
	close(h.next)
}
func (h *xargsHandoff) received() {
	h.mu.Lock()
	defer h.mu.Unlock()
	clearXargsWait(&h.parserUnblock)
	select {
	case h.space <- struct{}{}:
	default:
	}
}
func (h *xargsHandoff) childDone(done chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	close(done)
	if h.waitingChild == done {
		clearXargsWait(&h.dispatcherUnblock)
	}
}
func (h *xargsHandoff) block(ctx context.Context, incoming <-chan xargsNext, done <-chan struct{}) (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if incoming != nil && (len(h.next) > 0 || h.closed) {
		return func() {}, nil
	}
	select {
	case <-done:
		return func() {}, nil
	default:
	}
	unblock, err := h.activity.Block(ctx, commandprogram.WaitState{})
	if err != nil {
		return nil, err
	}
	h.dispatcherUnblock = unblock
	h.waitingInput = incoming != nil
	h.waitingChild = done
	return func() {
		h.mu.Lock()
		clearXargsWait(&h.dispatcherUnblock)
		h.waitingInput = false
		h.waitingChild = nil
		h.mu.Unlock()
	}, nil
}
