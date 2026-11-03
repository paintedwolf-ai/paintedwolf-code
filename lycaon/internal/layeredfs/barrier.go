package layeredfs

import (
	"context"
	"os"
	"sync"
)

// RegisterLowerProvider registers a custom or virtual LowerProvider for a RootID.
func (c *Core) RegisterLowerProvider(rootID string, provider LowerProvider) error {
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

// ReleaseView invalidates all handles, closes held descriptors, and marks the view released.
func (c *Core) ReleaseView() error {
	c.mu.Lock()
	if c.state.released {
		c.mu.Unlock()
		return nil
	}
	c.state.released = true
	handles := make([]*openHandle, 0, len(c.state.handles))
	for id, h := range c.state.handles {
		h.closed = true
		handles = append(handles, h)
		delete(c.state.handles, id)
	}
	c.state.nodesByID = make(map[NodeID]*privateNode)
	c.state.pathIndex = make(map[QualifiedPath]NodeID)
	c.state.dirCookies = make(map[uint64]dirCookieState)
	c.mu.Unlock()

	for _, h := range handles {
		if h.file != nil {
			_ = h.file.Close()
		}
		if h.roReader != nil {
			_ = h.roReader.Close()
		}
	}
	c.roots.close()
	c.backing.close()
	return nil
}

// BarrierToken identifies an acquired checkpoint barrier (#1658, #1663).
type BarrierToken struct {
	ViewID string
	Seq    uint64
}

type barrierCoordinator struct {
	viewID   string
	mu       sync.Mutex
	cond     *sync.Cond
	active   bool
	inFlight int64
	seq      uint64
}

func newBarrierCoordinator(viewID string) *barrierCoordinator {
	b := &barrierCoordinator{viewID: viewID}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *barrierCoordinator) enterMutation() error {
	b.mu.Lock()
	defer b.mu.Unlock()
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

// BeginCheckpointBarrier blocks new mutation admission, waits for active mutations to drain,
// and flushes all open writable handles and backing directories (#1658, #1663).
func (c *Core) BeginCheckpointBarrier(ctx context.Context) (BarrierToken, error) {
	c.mu.RLock()
	if c.state.released {
		c.mu.RUnlock()
		return BarrierToken{}, ErrViewReleased
	}
	c.mu.RUnlock()

	b := c.barrier
	b.mu.Lock()
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
		if err := ctx.Err(); err != nil {
			b.active = false
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
	c.mu.RLock()
	if c.state.released {
		c.mu.RUnlock()
		return ErrViewReleased
	}
	var files []*os.File
	for _, h := range c.state.handles {
		if !h.closed && h.file != nil {
			files = append(files, h.file)
		}
	}
	workFile := c.backing.workDirFile
	baseFile := c.backing.baseDirFile
	journalFile := c.backing.journalDirFile
	c.mu.RUnlock()

	for _, f := range files {
		if err := syncFD(int(f.Fd())); err != nil {
			return err
		}
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
