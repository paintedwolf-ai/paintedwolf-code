package fuselinux

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// mutationGate holds mutating requests while a checkpoint barrier is active and counts those
// in flight so the barrier can wait for them.
type mutationGate struct {
	mu       sync.Mutex
	cond     *sync.Cond
	closed   *atomic.Bool
	active   bool
	inFlight int64
	held     int64
}

func newMutationGate(closed *atomic.Bool) *mutationGate {
	g := &mutationGate{closed: closed}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// enter admits a mutating request, holding it while a barrier is active. The barrier ends on
// every path, and a held request stays cancellable by its own interrupt. Failing it instead
// would surface EAGAIN from blocking calls and lose kernel writeback.
func (g *mutationGate) enter(st *fuseRequestState) int32 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.held++
	defer func() { g.held-- }()
	for g.active && !g.closed.Load() && !st.isCanceled() {
		g.cond.Wait()
	}
	if g.closed.Load() {
		return -int32(syscall.EIO)
	}
	if st.isCanceled() {
		return -int32(syscall.EINTR)
	}
	g.inFlight++
	return 0
}

func (g *mutationGate) leave() {
	g.mu.Lock()
	g.inFlight--
	if g.inFlight <= 0 {
		g.inFlight = 0
		g.cond.Broadcast()
	}
	g.mu.Unlock()
}

func (g *mutationGate) wake() {
	g.mu.Lock()
	g.cond.Broadcast()
	g.mu.Unlock()
}

func (g *mutationGate) isActive() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}

func (g *mutationGate) open() {
	g.mu.Lock()
	g.active = false
	g.cond.Broadcast()
	g.mu.Unlock()
}

// closeAndDrain stops admission and waits for in-flight mutations. It leaves the gate open on failure.
func (g *mutationGate) closeAndDrain(ctx context.Context) error {
	g.mu.Lock()
	if g.active {
		g.mu.Unlock()
		return layeredfs.ErrBarrierActive
	}
	g.active = true
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			g.wake()
		case <-done:
		}
	}()
	defer g.mu.Unlock()
	for g.inFlight > 0 {
		var err error
		if g.closed.Load() {
			err = errors.New("fuselinux: server closed during barrier drain")
		} else {
			err = ctx.Err()
		}
		if err != nil {
			g.active = false
			g.cond.Broadcast()
			return err
		}
		g.cond.Wait()
	}
	return nil
}

// BeginBarrierDrain blocks admission of new mutating FUSE requests, waits for in-flight
// mutating FUSE requests to finish, and flushes Core journal state before Core.BeginCheckpointBarrier.
func (s *Server) BeginBarrierDrain(ctx context.Context, viewID string) (func(), error) {
	if !s.IsAlive() {
		return nil, errors.New("fuselinux: FUSE server is not alive")
	}
	if err := s.gate.closeAndDrain(ctx); err != nil {
		return nil, err
	}
	if err := s.FlushForCheckpoint(ctx, viewID); err != nil {
		s.gate.open()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(s.gate.open) }, nil
}

// FlushForCheckpoint syncs all open handles tracked by the FUSE server and snapshots Core's journal.
func (s *Server) FlushForCheckpoint(_ context.Context, _ string) error {
	if !s.IsAlive() {
		return errors.New("fuselinux: FUSE server is not alive")
	}
	s.nodes.mu.RLock()
	hids := make([]layeredfs.HandleID, 0, len(s.nodes.openHandles))
	for _, h := range s.nodes.openHandles {
		if !h.isDir && h.hid != 0 {
			hids = append(hids, h.hid)
		}
	}
	s.nodes.mu.RUnlock()
	for _, hid := range hids {
		if err := s.core.SyncHandle(hid); err != nil && !errors.Is(err, layeredfs.ErrStaleHandle) {
			return err
		}
	}
	_, err := s.core.SnapshotJournal()
	return err
}

// markClosed ends admission and wakes every waiter. Both Close and a dead connection reach it.
func (s *Server) markClosed() {
	s.closed.Store(true)
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.gate.wake()
	s.locks.mu.Lock()
	s.locks.cond.Broadcast()
	s.locks.mu.Unlock()
}

// Close stops the FUSE server and closes /dev/fuse, including after the connection ended by itself.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.markClosed()
		if s.stopPipe[1] >= 0 {
			closeRawFD(s.stopPipe[1])
		}
		s.wg.Wait()
		if s.stopPipe[0] >= 0 {
			closeRawFD(s.stopPipe[0])
		}
		s.notifyMu.Lock()
		closeRawFD(s.fd)
		s.notifyMu.Unlock()
	})
	return nil
}
