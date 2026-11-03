package fuselinux

import (
	"context"
	"errors"
	"sync"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// enterMutation admits a mutating request, holding it while a checkpoint barrier is active.
// The barrier ends on every path, and a held request stays cancellable by its own interrupt.
// Failing it instead would surface EAGAIN from blocking calls and lose kernel writeback.
func (s *Server) enterMutation(st *fuseRequestState) int32 {
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	s.gatedMut++
	defer func() { s.gatedMut-- }()
	for s.barrierActive && !s.closed.Load() && !st.isCanceled() {
		s.gateCond.Wait()
	}
	if s.closed.Load() {
		return -int32(syscall.EIO)
	}
	if st.isCanceled() {
		return -int32(syscall.EINTR)
	}
	s.inFlightMut++
	return 0
}

func (s *Server) leaveMutation() {
	s.gateMu.Lock()
	s.inFlightMut--
	if s.inFlightMut <= 0 {
		s.inFlightMut = 0
		s.gateCond.Broadcast()
	}
	s.gateMu.Unlock()
}

// BeginBarrierDrain blocks admission of new mutating FUSE requests, waits for in-flight
// mutating FUSE requests to finish, and flushes Core journal state before Core.BeginCheckpointBarrier.
func (s *Server) BeginBarrierDrain(ctx context.Context, viewID string) (func(), error) {
	if !s.IsAlive() {
		return nil, errors.New("fuselinux: FUSE server is not alive")
	}
	s.gateMu.Lock()
	if s.barrierActive {
		s.gateMu.Unlock()
		return nil, layeredfs.ErrBarrierActive
	}
	s.barrierActive = true
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.gateMu.Lock()
			s.gateCond.Broadcast()
			s.gateMu.Unlock()
		case <-done:
		}
	}()
	for s.inFlightMut > 0 {
		if s.closed.Load() {
			s.barrierActive = false
			s.gateCond.Broadcast()
			s.gateMu.Unlock()
			close(done)
			return nil, errors.New("fuselinux: server closed during barrier drain")
		}
		if err := ctx.Err(); err != nil {
			s.barrierActive = false
			s.gateCond.Broadcast()
			s.gateMu.Unlock()
			close(done)
			return nil, err
		}
		s.gateCond.Wait()
	}
	s.gateMu.Unlock()
	close(done)

	if err := s.FlushForCheckpoint(ctx, viewID); err != nil {
		s.gateMu.Lock()
		s.barrierActive = false
		s.gateCond.Broadcast()
		s.gateMu.Unlock()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.gateMu.Lock()
			s.barrierActive = false
			s.gateCond.Broadcast()
			s.gateMu.Unlock()
		})
	}, nil
}

// FlushForCheckpoint syncs all open handles tracked by the FUSE server and snapshots Core's journal.
func (s *Server) FlushForCheckpoint(_ context.Context, _ string) error {
	if !s.IsAlive() {
		return errors.New("fuselinux: FUSE server is not alive")
	}
	s.nodeMu.RLock()
	hids := make([]layeredfs.HandleID, 0, len(s.openHandles))
	for _, h := range s.openHandles {
		if !h.isDir && h.hid != 0 {
			hids = append(hids, h.hid)
		}
	}
	s.nodeMu.RUnlock()
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
	s.gateMu.Lock()
	s.gateCond.Broadcast()
	s.gateMu.Unlock()
	s.lockMu.Lock()
	s.lockCond.Broadcast()
	s.lockMu.Unlock()
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
