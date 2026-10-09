package turnadmission

import (
	"context"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/session/submissions"
)

// roundEndDrains coalesces round-end dispatch of queued prompts per session.
type roundEndDrains struct {
	mu        sync.Mutex
	bySession map[string]*roundEndDrain
	wg        sync.WaitGroup
}

type roundEndDrain struct {
	cancel context.CancelFunc
	rerun  bool
}

// start registers a pass, or asks the running pass to rerun once.
func (d *roundEndDrains) start(parent context.Context, sessionID string) (context.Context, *roundEndDrain) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if work := d.bySession[sessionID]; work != nil {
		work.rerun = true
		return nil, nil
	}
	if d.bySession == nil {
		d.bySession = make(map[string]*roundEndDrain)
	}
	ctx, cancel := context.WithCancel(parent)
	work := &roundEndDrain{cancel: cancel}
	d.bySession[sessionID] = work
	d.wg.Add(1)
	return ctx, work
}

// next consumes a rerun request, or retires the pass so a later start runs.
func (d *roundEndDrains) next(sessionID string, work *roundEndDrain) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.bySession[sessionID] != work {
		return false
	}
	if work.rerun {
		work.rerun = false
		return true
	}
	delete(d.bySession, sessionID)
	return false
}

// finish retires a pass that exited without consulting next.
func (d *roundEndDrains) finish(sessionID string, work *roundEndDrain) {
	d.mu.Lock()
	if d.bySession[sessionID] == work {
		delete(d.bySession, sessionID)
	}
	d.mu.Unlock()
	work.cancel()
	d.wg.Done()
}

func (d *roundEndDrains) cancelAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, work := range d.bySession {
		work.cancel()
	}
}

func (d *roundEndDrains) wait(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-ctx.Done():
	}
	d.cancelAll()
	<-done
}

// drainQueuedPromptsAtRoundEnd dispatches receipts the turn-end check left
// queued because the round was still open.
func (m *Service) RoundEnd(ctx context.Context, sessionID string) {
	if m == nil || m.store == nil || m.Gate.InProgress(ctx, sessionID) {
		return
	}
	queued, err := m.store.ListQueuedUserPromptSubmissions(ctx, sessionID)
	if err != nil {
		slog.ErrorContext(ctx, "read queued prompts at round end", "session_id", sessionID, "error", err)
		return
	}
	if len(queued) == 0 || !m.RoundComplete(ctx, sessionID) {
		return
	}
	// The notifying goroutine may hold the dispatch lane, or carry a lane
	// marker copied from another goroutine; this pass takes its own lane.
	parent := context.WithoutCancel(ctx)
	parent = submissions.Detached(parent)
	drainCtx, work := m.roundEndDrains.start(parent, sessionID)
	if work == nil {
		return
	}
	go func() {
		defer m.roundEndDrains.finish(sessionID, work)
		defer observability.GuardPanic("session.round_end_drain")
		for {
			if err := m.Submissions.DrainPromptSubmissions(drainCtx, sessionID); err != nil {
				LogTurnFailure(drainCtx, sessionID, err)
			}
			if !m.roundEndDrains.next(sessionID, work) {
				return
			}
		}
	}()
}
