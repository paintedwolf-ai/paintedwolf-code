package layeredfs

import (
	"context"
	"os"
	"sync"
)

// RegisterLowerProvider registers a custom or virtual LowerProvider for a RootID.
func (c *Core) RegisterLowerProvider(rootID string, provider LowerProvider) error {
	if err := c.barrier.enterMutation(); err != nil {
		return err
	}
	defer c.barrier.leaveMutation()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return ErrViewReleased
	}
	if _, ok := c.roots.byID[rootID]; !ok {
		return ErrInvalidRoot
	}
	c.roots.providers[rootID] = provider
	return nil
}

// ViewID returns the unique view identifier.
func (c *Core) ViewID() string {
	return c.cfg.ViewID
}

// Stats returns a snapshot of the deterministic operation and byte counters.
func (c *Core) Stats() Stats {
	c.mu.RLock()
	logical := countLogicalChangesLocked(c)
	c.mu.RUnlock()
	return Stats{
		LowerDirReads:     c.counters.lowerDirReads.Load(),
		LowerLookups:      c.counters.lowerLookups.Load(),
		PhysicalCopyUps:   c.counters.physicalCopyUps.Load(),
		LogicalChanges:    int64(logical),
		CapturedBaseBytes: c.counters.capturedBaseBytes.Load(),
		JournalEntries:    c.counters.journalEntries.Load(),
	}
}

// ResetStepStats clears per-scenario lower read/lookup counters while preserving view state.
func (c *Core) ResetStepStats() {
	c.counters.lowerDirReads.Store(0)
	c.counters.lowerLookups.Store(0)
}

// ReleaseView closes admission to new operations, waits for all in-flight mutations, reads,
// and flushes to drain and settle their accounting, flushes and closes open handles and
// directory descriptors, and preserves settled recovery state.
func (c *Core) ReleaseView() error {
	alreadyReleased, waitOnly := c.barrier.beginRelease()
	if alreadyReleased {
		return nil
	}
	if waitOnly {
		c.barrier.waitReleaseDone()
		return nil
	}

	c.mu.Lock()
	c.state.seq.closing = true
	c.mu.Unlock()

	c.barrier.waitDrain()

	c.mu.Lock()
	handles := make([]*openHandle, 0, len(c.state.handles))
	for id, h := range c.state.handles {
		h.closed = true
		handles = append(handles, h)
		delete(c.state.handles, id)
	}
	c.state.dirCookies = make(map[uint64]dirCookieState)
	hasRecoveryState := len(c.state.journal) > 0 || len(c.state.pathIndex) > 0 || len(c.state.deletedPaths) > 0
	c.mu.Unlock()

	for _, h := range handles {
		if h.file != nil {
			_ = syncFD(int(h.file.Fd()))
		}
		_ = releaseHandleRef(c, h)
	}
	for _, df := range []*os.File{c.backing.workDirFile, c.backing.baseDirFile, c.backing.journalDirFile} {
		if df != nil {
			_ = syncFD(int(df.Fd()))
		}
	}
	if hasRecoveryState {
		_, _ = snapshotJournal(c)
	}

	c.mu.Lock()
	c.state.released = true
	c.mu.Unlock()

	c.roots.close()
	c.backing.close()
	c.barrier.finishRelease()
	return nil
}

// BarrierToken identifies an acquired checkpoint barrier (#1658, #1663).
type BarrierToken struct {
	ViewID string
	Seq    uint64
}

type barrierCoordinator struct {
	viewID      string
	mu          sync.Mutex
	cond        *sync.Cond
	active      bool
	closing     bool
	released    bool
	inFlight    int64
	opsInFlight int64
	seq         uint64
}

func newBarrierCoordinator(viewID string) *barrierCoordinator {
	b := &barrierCoordinator{viewID: viewID}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *barrierCoordinator) enterMutation() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closing || b.released {
		return ErrViewReleased
	}
	if b.active {
		return ErrBarrierActive
	}
	b.inFlight++
	return nil
}

func (b *barrierCoordinator) leaveMutation() {
	b.mu.Lock()
	b.inFlight--
	if b.inFlight <= 0 {
		b.inFlight = 0
		b.cond.Broadcast()
	}
	b.mu.Unlock()
}

func (b *barrierCoordinator) enterOp() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closing || b.released {
		return ErrViewReleased
	}
	b.opsInFlight++
	return nil
}

func (b *barrierCoordinator) leaveOp() {
	b.mu.Lock()
	b.opsInFlight--
	if b.opsInFlight <= 0 {
		b.opsInFlight = 0
		b.cond.Broadcast()
	}
	b.mu.Unlock()
}

func (b *barrierCoordinator) beginRelease() (bool, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.released {
		return true, false
	}
	if b.closing {
		return false, true
	}
	b.closing = true
	b.cond.Broadcast()
	return false, false
}

func (b *barrierCoordinator) waitDrain() {
	b.mu.Lock()
	for b.inFlight > 0 || b.opsInFlight > 0 || b.active {
		b.cond.Wait()
	}
	b.mu.Unlock()
}

func (b *barrierCoordinator) finishRelease() {
	b.mu.Lock()
	b.released = true
	b.cond.Broadcast()
	b.mu.Unlock()
}

func (b *barrierCoordinator) waitReleaseDone() {
	b.mu.Lock()
	for !b.released {
		b.cond.Wait()
	}
	b.mu.Unlock()
}

// BeginCheckpointBarrier blocks new mutation admission, waits for active mutations to drain,
// and flushes all open writable handles and backing directories (#1658, #1663).
func (c *Core) BeginCheckpointBarrier(ctx context.Context) (BarrierToken, error) {
	b := c.barrier
	b.mu.Lock()
	if b.closing || b.released {
		b.mu.Unlock()
		return BarrierToken{}, ErrViewReleased
	}
	if b.active {
		b.mu.Unlock()
		return BarrierToken{}, ErrBarrierActive
	}
	b.active = true
	b.seq++
	seq := b.seq

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			b.mu.Lock()
			b.cond.Broadcast()
			b.mu.Unlock()
		case <-done:
		}
	}()

	for b.inFlight > 0 {
		if b.closing || b.released {
			b.active = false
			b.cond.Broadcast()
			b.mu.Unlock()
			close(done)
			return BarrierToken{}, ErrViewReleased
		}
		if err := ctx.Err(); err != nil {
			b.active = false
			b.cond.Broadcast()
			b.mu.Unlock()
			close(done)
			return BarrierToken{}, err
		}
		b.cond.Wait()
	}
	b.mu.Unlock()
	close(done)

	if err := flushBackingState(c); err != nil {
		_ = c.EndCheckpointBarrier(BarrierToken{ViewID: c.cfg.ViewID, Seq: seq})
		return BarrierToken{}, err
	}
	return BarrierToken{ViewID: c.cfg.ViewID, Seq: seq}, nil
}

// EndCheckpointBarrier releases an active checkpoint barrier on every success or failure path.
func (c *Core) EndCheckpointBarrier(token BarrierToken) error {
	b := c.barrier
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.active || token.ViewID != b.viewID || token.Seq != b.seq {
		return ErrBarrierActive
	}
	b.active = false
	b.cond.Broadcast()
	return nil
}

func flushBackingState(c *Core) error {
	c.mu.Lock()
	if c.state.released {
		c.mu.Unlock()
		return ErrViewReleased
	}
	var held []*openHandle
	for _, h := range c.state.handles {
		if !h.closed && h.file != nil {
			h.refs++
			held = append(held, h)
		}
	}
	workFile := c.backing.workDirFile
	baseFile := c.backing.baseDirFile
	journalFile := c.backing.journalDirFile
	c.mu.Unlock()

	var firstErr error
	for _, h := range held {
		if err := syncFD(int(h.file.Fd())); err != nil && firstErr == nil {
			firstErr = err
		}
		_ = releaseHandleRef(c, h)
	}
	if firstErr != nil {
		return firstErr
	}
	for _, df := range []*os.File{workFile, baseFile, journalFile} {
		if df != nil {
			if err := syncFD(int(df.Fd())); err != nil {
				return err
			}
		}
	}
	return nil
}
