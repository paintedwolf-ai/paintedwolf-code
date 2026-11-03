//go:build linux

package fuselinux

import (
	"encoding/binary"
	"syscall"
)

const (
	fuseHeaderInSize  = 40
	fuseOpLookup      = 1
	fuseOpForget      = 2
	fuseOpGetattr     = 3
	fuseOpSetattr     = 4
	fuseOpReadlink    = 5
	fuseOpSymlink     = 6
	fuseOpMknod       = 8
	fuseOpMkdir       = 9
	fuseOpUnlink      = 10
	fuseOpRmdir       = 11
	fuseOpRename      = 12
	fuseOpLink        = 13
	fuseOpOpen        = 14
	fuseOpRead        = 15
	fuseOpWrite       = 16
	fuseOpStatfs      = 17
	fuseOpRelease     = 18
	fuseOpFsync       = 20
	fuseOpSetxattr    = 21
	fuseOpGetxattr    = 22
	fuseOpListxattr   = 23
	fuseOpRemovexattr = 24
	fuseOpFlush       = 25
	fuseOpInit        = 26
	fuseOpOpendir     = 27
	fuseOpReaddir     = 28
	fuseOpReleasedir  = 29
	fuseOpFsyncdir    = 30
	fuseOpGetlk       = 31
	fuseOpSetlk       = 32
	fuseOpSetlkw      = 33
	fuseOpAccess      = 34
	fuseOpCreate      = 35
	fuseOpInterrupt   = 36
	fuseOpDestroy     = 38
	fuseOpBatchForget = 42
	fuseOpRename2     = 45
	fuseOpLseek       = 46
)

// requestLane says how the single /dev/fuse reader admits a request.
type requestLane int

const (
	// laneInline requests touch only server memory and never wait, so the reader serves
	// them itself. Unlock and lock release therefore need no slot that a waiter could hold.
	laneInline requestLane = iota
	// laneLockWait requests may block on a conflicting lock. They hold a waiter slot only,
	// so waiters can never starve the file operations a lock holder needs to reach its unlock.
	laneLockWait
	// laneControl requests release backing resources and take one of a few control slots.
	laneControl
	// laneOrdinary requests queue for the fixed worker pool. A full queue stops the reader,
	// which is safe because ordinary requests never wait on another FUSE request.
	laneOrdinary
)

func classifyRequest(opcode uint32, body []byte) requestLane {
	switch opcode {
	case fuseOpInit, fuseOpDestroy, fuseOpFlush, fuseOpReleasedir, fuseOpGetlk, fuseOpSetlk:
		return laneInline
	case fuseOpRelease:
		return laneControl
	case fuseOpSetlkw:
		if len(body) >= 36 && binary.LittleEndian.Uint32(body[32:36]) == syscall.F_UNLCK {
			return laneInline
		}
		return laneLockWait
	default:
		return laneOrdinary
	}
}

// dispatchRequest runs on the reader. It registers each request before any handler can run,
// so an interrupt read later always finds a target that has not been answered.
func (s *Server) dispatchRequest(req []byte) {
	if len(req) < fuseHeaderInSize {
		return
	}
	opcode := binary.LittleEndian.Uint32(req[4:8])
	unique := binary.LittleEndian.Uint64(req[8:16])
	body := req[fuseHeaderInSize:]
	switch opcode {
	case fuseOpForget, fuseOpBatchForget:
		return
	case fuseOpInterrupt:
		s.handleInterrupt(unique, body)
		return
	}

	st := s.requests.register(unique)
	switch classifyRequest(opcode, body) {
	case laneInline:
		s.serveRequest(st, req)
	case laneLockWait:
		s.startSetlkw(st, req)
	case laneControl:
		select {
		case s.controlSem <- struct{}{}:
		case <-s.stopCh:
			return
		}
		s.wg.Add(1)
		go func() {
			defer func() {
				<-s.controlSem
				s.wg.Done()
			}()
			s.serveRequest(st, req)
		}()
	default:
		select {
		case s.ordinaryQueue <- queuedRequest{state: st, req: req}:
		case <-s.stopCh:
		}
	}
}

func (s *Server) ordinaryWorker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopCh:
			return
		case q := <-s.ordinaryQueue:
			if q.state.isCanceled() {
				_ = s.writeReply(q.state.unique, -int32(syscall.EINTR), nil)
				s.requests.unregister(q.state.unique)
				continue
			}
			s.serveRequest(q.state, q.req)
		}
	}
}

// handleInterrupt cancels only the identified request. An unknown target has already been
// answered: the kernel discards EAGAIN for a finished request and requeues the interrupt
// for one it still holds, so a reordered interrupt is retried rather than lost.
func (s *Server) handleInterrupt(interruptUnique uint64, body []byte) {
	if len(body) < 8 {
		return
	}
	if !s.cancelRequest(binary.LittleEndian.Uint64(body[0:8])) {
		_ = s.writeReply(interruptUnique, -int32(syscall.EAGAIN), nil)
	}
}

func (s *Server) cancelRequest(unique uint64) bool {
	if !s.requests.markCanceled(unique) {
		return false
	}
	s.locks.mu.Lock()
	s.locks.cond.Broadcast()
	s.locks.mu.Unlock()
	s.gate.wake()
	return true
}
