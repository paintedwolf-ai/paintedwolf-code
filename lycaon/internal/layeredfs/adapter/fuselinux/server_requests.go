package fuselinux

import (
	"encoding/binary"
	"sort"
	"sync/atomic"
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

type fuseRequestState struct {
	unique      uint64
	opcode      uint32
	nodeID      uint64
	controlLane bool
	waitStart   uint64
	waitEnd     uint64
	lockWaiting atomic.Bool
	canceled    atomic.Bool
}

func (r *fuseRequestState) IsCanceled() bool {
	return r != nil && r.canceled.Load()
}

// isControlLaneRequest classifies FUSE operations that release locks/handles,
// deliver interrupts, or query lock status so they use the dedicated control lane
// and cannot be starved when general request slots are saturated by blocked lock waiters.
func isControlLaneRequest(opcode uint32, body []byte) bool {
	switch opcode {
	case fuseOpInterrupt, fuseOpFlush, fuseOpRelease, fuseOpReleasedir,
		fuseOpGetlk, fuseOpSetlk, fuseOpInit, fuseOpStatfs, fuseOpAccess, fuseOpDestroy:
		return true
	case fuseOpSetlkw:
		if len(body) >= 36 {
			lkType := binary.LittleEndian.Uint32(body[32:36])
			return lkType == syscall.F_UNLCK
		}
		return false
	default:
		return false
	}
}

func registerRequest(s *Server, unique uint64, opcode uint32, nodeID uint64, controlLane bool) *fuseRequestState {
	st := &fuseRequestState{
		unique:      unique,
		opcode:      opcode,
		nodeID:      nodeID,
		controlLane: controlLane,
	}
	if unique == 0 {
		return st
	}
	s.reqMu.Lock()
	if _, interrupted := s.interruptedReqs[unique]; interrupted {
		delete(s.interruptedReqs, unique)
		st.canceled.Store(true)
	}
	s.inFlightReqs[unique] = st
	s.reqMu.Unlock()
	return st
}

func unregisterRequest(s *Server, unique uint64) {
	if unique == 0 {
		return
	}
	s.reqMu.Lock()
	delete(s.inFlightReqs, unique)
	delete(s.interruptedReqs, unique)
	s.reqMu.Unlock()
}

func handleInterrupt(s *Server, body []byte) {
	if len(body) < 8 {
		return
	}
	targetUnique := binary.LittleEndian.Uint64(body[0:8])
	s.CancelRequest(targetUnique)
}

// SendInterrupt dispatches a FUSE_INTERRUPT wire request targeting targetUnique through the server's control lane.
func (s *Server) SendInterrupt(targetUnique uint64) {
	pkt := make([]byte, fuseHeaderInSize+8)
	binary.LittleEndian.PutUint32(pkt[0:4], uint32(len(pkt)))
	binary.LittleEndian.PutUint32(pkt[4:8], fuseOpInterrupt)
	binary.LittleEndian.PutUint64(pkt[8:16], ^uint64(0)-targetUnique)
	binary.LittleEndian.PutUint64(pkt[fuseHeaderInSize:], targetUnique)
	dispatchIncomingRequest(s, pkt)
}

func dispatchIncomingRequest(s *Server, req []byte) {
	if len(req) < fuseHeaderInSize {
		return
	}
	opcode := binary.LittleEndian.Uint32(req[4:8])
	if opcode == fuseOpForget || opcode == fuseOpBatchForget {
		return
	}
	if opcode == fuseOpInterrupt {
		handleInterrupt(s, req[fuseHeaderInSize:])
		return
	}
	unique := binary.LittleEndian.Uint64(req[8:16])
	body := req[fuseHeaderInSize:]
	controlLane := isControlLaneRequest(opcode, body)
	if controlLane {
		select {
		case s.controlSem <- struct{}{}:
		default:
			if s.closed.Load() {
				return
			}
			s.controlSem <- struct{}{}
		}
		s.wg.Add(1)
		go func(r []byte) {
			defer func() {
				<-s.controlSem
				s.wg.Done()
			}()
			s.handleRequest(r, true)
		}(req)
		return
	}

	select {
	case s.workerSem <- struct{}{}:
	default:
		_ = s.writeReply(unique, -int32(syscall.EAGAIN), nil)
		return
	}
	s.wg.Add(1)
	go func(r []byte) {
		defer func() {
			<-s.workerSem
			s.wg.Done()
		}()
		s.handleRequest(r, false)
	}(req)
}

// CancelRequest marks the in-flight FUSE request identified by unique as canceled/interrupted
// and wakes any blocked lock wait so only that specific request terminates with EINTR.
func (s *Server) CancelRequest(unique uint64) bool {
	if s == nil || unique == 0 {
		return false
	}
	s.reqMu.Lock()
	st, ok := s.inFlightReqs[unique]
	if ok {
		st.canceled.Store(true)
	} else {
		if len(s.interruptedReqs) >= 256 {
			for k := range s.interruptedReqs {
				delete(s.interruptedReqs, k)
				break
			}
		}
		s.interruptedReqs[unique] = struct{}{}
	}
	s.reqMu.Unlock()

	s.lockMu.Lock()
	s.lockCond.Broadcast()
	s.lockMu.Unlock()
	return ok
}

// BlockedLockWaiters returns the sorted FUSE request unique IDs currently blocked in F_SETLKW.
func (s *Server) BlockedLockWaiters() []uint64 {
	if s == nil {
		return nil
	}
	s.reqMu.Lock()
	var out []uint64
	for u, st := range s.inFlightReqs {
		if st.lockWaiting.Load() && !st.IsCanceled() {
			out = append(out, u)
		}
	}
	s.reqMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// BlockedLockWaiterForRange returns the unique ID of a blocked F_SETLKW request waiting on [start, end].
func (s *Server) BlockedLockWaiterForRange(start, end uint64) (uint64, bool) {
	if s == nil {
		return 0, false
	}
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	for u, st := range s.inFlightReqs {
		if st.lockWaiting.Load() && !st.IsCanceled() && st.waitStart == start && st.waitEnd == end {
			return u, true
		}
	}
	return 0, false
}

// ActiveRequestCount returns the number of currently active in-flight FUSE requests.
func (s *Server) ActiveRequestCount() int {
	if s == nil {
		return 0
	}
	s.reqMu.Lock()
	n := len(s.inFlightReqs)
	s.reqMu.Unlock()
	return n
}
