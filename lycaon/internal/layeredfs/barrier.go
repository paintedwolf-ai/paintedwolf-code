package layeredfs

import (
	"context"
	"errors"
	"os"
	"sort"
	"sync"
)

// ReleaseView closes admission to new operations, waits for all in-flight mutations, reads,
// and flushes to drain and settle their accounting, flushes and closes open handles and
// directory descriptors, persists the recovery journal snapshot, and releases view resources.
// If any durability or snapshot step fails, ReleaseView preserves settled recovery state and
// returns the error so the caller can retry or inspect recovery state.
func (c *Core) ReleaseView() error {
	alreadyReleased, joinedAttempt, att := c.barrier.beginRelease()
	if alreadyReleased {
		return nil
	}
	if joinedAttempt {
		return waitReleaseAttempt(c, att)
	}

	c.mu.Lock()
	c.state.seq.closing = true
	c.mu.Unlock()

	if c.cfg.FaultHooks != nil && c.cfg.FaultHooks.ReleaseWaitHook != nil {
		c.cfg.FaultHooks.ReleaseWaitHook("draining")
	}
	c.barrier.waitDrain()

	var releaseErr error

	c.mu.Lock()
	type handleEntry struct {
		id HandleID
		h  *openHandle
	}
	entries := make([]handleEntry, 0, len(c.state.handles))
	for id, h := range c.state.handles {
		entries = append(entries, handleEntry{id: id, h: h})
	}
	hasRecoveryState := len(c.state.journal) > 0 || len(c.state.pathIndex) > 0 || len(c.state.deletedPaths) > 0 || len(c.state.basesByPath) > 0
	c.mu.Unlock()

	for _, e := range entries {
		var sErr error
		if e.h.file != nil {
			sErr = errors.Join(runJournalSyncHook(c, "handle_sync"), flushFD(c, int(e.h.file.Fd())))
		}
		if sErr != nil {
			releaseErr = errors.Join(releaseErr, sErr)
			continue
		}
		c.mu.Lock()
		e.h.closed = true
		delete(c.state.handles, e.id)
		c.mu.Unlock()
		releaseErr = errors.Join(releaseErr, releaseHandleRef(c, e.h))
	}

	if c.backing.workDirFile != nil {
		releaseErr = errors.Join(releaseErr, runJournalSyncHook(c, "work_dir_sync"), flushFD(c, int(c.backing.workDirFile.Fd())))
	}
	if c.backing.baseDirFile != nil {
		releaseErr = errors.Join(releaseErr, runJournalSyncHook(c, "base_dir_sync"), flushFD(c, int(c.backing.baseDirFile.Fd())))
	}

	if hasRecoveryState {
		_, snapErr := snapshotJournal(c)
		releaseErr = errors.Join(releaseErr, snapErr)
	} else if c.backing.journalDirFile != nil {
		releaseErr = errors.Join(releaseErr, runJournalSyncHook(c, "journal_dir_sync"), flushFD(c, int(c.backing.journalDirFile.Fd())))
	}

	if releaseErr != nil {
		c.barrier.endReleaseAttempt(att, releaseErr)
		return releaseErr
	}

	c.mu.Lock()
	c.state.dirCookies = make(map[uint64]dirCookieState)
	c.state.released = true
	c.mu.Unlock()

	c.roots.close()
	c.backing.close()
	c.barrier.endReleaseAttempt(att, nil)
	return nil
}

// BarrierToken identifies an acquired checkpoint barrier (#1658, #1663).
type BarrierToken struct {
	ViewID string
	Seq    uint64
}

type releaseAttempt struct {
	done chan struct{}
	err  error
}

type barrierCoordinator struct {
	viewID      string
	mu          sync.Mutex
	cond        *sync.Cond
	active      bool
	closing     bool
	released    bool
	curAttempt  *releaseAttempt
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

func (b *barrierCoordinator) beginRelease() (bool, bool, *releaseAttempt) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.released {
		return true, false, nil
	}
	if b.curAttempt != nil {
		return false, true, b.curAttempt
	}
	att := &releaseAttempt{done: make(chan struct{})}
	b.closing = true
	b.curAttempt = att
	b.cond.Broadcast()
	return false, false, att
}

func (b *barrierCoordinator) waitDrain() {
	b.mu.Lock()
	for b.inFlight > 0 || b.opsInFlight > 0 || b.active {
		b.cond.Wait()
	}
	b.mu.Unlock()
}

func (b *barrierCoordinator) endReleaseAttempt(att *releaseAttempt, err error) {
	b.mu.Lock()
	att.err = err
	close(att.done)
	b.curAttempt = nil
	if err == nil {
		b.released = true
	}
	b.cond.Broadcast()
	b.mu.Unlock()
}

func waitReleaseAttempt(c *Core, att *releaseAttempt) error {
	if c.cfg.FaultHooks != nil && c.cfg.FaultHooks.ReleaseWaitHook != nil {
		c.cfg.FaultHooks.ReleaseWaitHook("joined")
	}
	<-att.done
	if c.cfg.FaultHooks != nil && c.cfg.FaultHooks.ReleaseWaitHook != nil {
		c.cfg.FaultHooks.ReleaseWaitHook("done")
	}
	return att.err
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
	flushedPaths := make(map[string]struct{}, len(c.state.handles))
	for _, h := range c.state.handles {
		if !h.closed && h.file != nil {
			h.refs++
			held = append(held, h)
			if node := c.state.nodesByID[h.nodeID]; node != nil && node.flags.backingPath != "" {
				flushedPaths[node.flags.backingPath] = struct{}{}
			}
		}
	}
	var dirtyPaths []string
	for p := range c.state.dirtyBackingFiles {
		if _, already := flushedPaths[p]; !already {
			dirtyPaths = append(dirtyPaths, p)
		}
	}
	sort.Strings(dirtyPaths)
	workFile := c.backing.workDirFile
	baseFile := c.backing.baseDirFile
	journalFile := c.backing.journalDirFile
	c.mu.Unlock()

	var flushErr error
	for _, h := range held {
		flushErr = errors.Join(flushErr, runJournalSyncHook(c, "handle_sync"), flushFD(c, int(h.file.Fd())))
		flushErr = errors.Join(flushErr, releaseHandleRef(c, h))
	}
	for _, p := range dirtyPaths {
		f, err := os.Open(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			flushErr = errors.Join(flushErr, err)
			continue
		}
		flushErr = errors.Join(flushErr, flushFD(c, int(f.Fd())), f.Close())
	}
	if flushErr != nil {
		return flushErr
	}
	c.mu.Lock()
	clear(c.state.dirtyBackingFiles)
	c.mu.Unlock()
	if workFile != nil {
		flushErr = errors.Join(flushErr, runJournalSyncHook(c, "work_dir_sync"), flushFD(c, int(workFile.Fd())))
	}
	if baseFile != nil {
		flushErr = errors.Join(flushErr, runJournalSyncHook(c, "base_dir_sync"), flushFD(c, int(baseFile.Fd())))
	}
	if journalFile != nil {
		flushErr = errors.Join(flushErr, runJournalSyncHook(c, "journal_dir_sync"), flushFD(c, int(journalFile.Fd())))
	}
	return flushErr
}
