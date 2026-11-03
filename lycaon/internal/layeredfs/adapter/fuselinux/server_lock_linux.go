//go:build linux

package fuselinux

import (
	"encoding/binary"
	"sort"
	"syscall"
)

const (
	fuseLkFlock            uint32 = 1 << 0
	fuseReleaseFlush       uint32 = 1 << 0
	fuseReleaseFlockUnlock uint32 = 1 << 1
)

func parseFuseLockIn(body []byte) (fileLockEntry, bool) {
	if len(body) < 40 {
		return fileLockEntry{}, false
	}
	fh := binary.LittleEndian.Uint64(body[0:8])
	owner := binary.LittleEndian.Uint64(body[8:16])
	start := binary.LittleEndian.Uint64(body[16:24])
	end := binary.LittleEndian.Uint64(body[24:32])
	lkType := binary.LittleEndian.Uint32(body[32:36])
	pid := binary.LittleEndian.Uint32(body[36:40])
	isFlock := len(body) >= 44 && binary.LittleEndian.Uint32(body[40:44])&fuseLkFlock != 0
	if !isFlock && end < start {
		return fileLockEntry{}, false
	}
	return fileLockEntry{
		owner:   owner,
		fh:      fh,
		start:   start,
		end:     end,
		typ:     lkType,
		pid:     pid,
		isFlock: isFlock,
	}, true
}

func rangesOverlap(aStart, aEnd, bStart, bEnd uint64) bool {
	return aStart <= bEnd && bStart <= aEnd
}

func locksConflict(req, existing fileLockEntry) bool {
	if req.isFlock != existing.isFlock {
		return false
	}
	if req.owner == existing.owner {
		return false
	}
	if req.typ != syscall.F_WRLCK && existing.typ != syscall.F_WRLCK {
		return false
	}
	if req.isFlock {
		return true
	}
	return rangesOverlap(req.start, req.end, existing.start, existing.end)
}

func (s *Server) opGetlk(nodeID uint64, body []byte) ([]byte, int32) {
	req, ok := parseFuseLockIn(body)
	if !ok {
		return nil, -int32(syscall.EINVAL)
	}
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	if req.typ != syscall.F_UNLCK {
		for _, existing := range s.fileLocks[nodeID] {
			if locksConflict(req, existing) {
				out := make([]byte, 24)
				binary.LittleEndian.PutUint64(out[0:8], existing.start)
				binary.LittleEndian.PutUint64(out[8:16], existing.end)
				binary.LittleEndian.PutUint32(out[16:20], existing.typ)
				binary.LittleEndian.PutUint32(out[20:24], existing.pid)
				return out, 0
			}
		}
	}
	out := make([]byte, 24)
	binary.LittleEndian.PutUint32(out[16:20], syscall.F_UNLCK)
	return out, 0
}

// opSetlk serves the requests that never wait: non-blocking acquires and every unlock.
func (s *Server) opSetlk(st *fuseRequestState, nodeID uint64, body []byte) ([]byte, int32) {
	req, ok := parseFuseLockIn(body)
	if !ok {
		return nil, -int32(syscall.EINVAL)
	}
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	errno, conflict := s.attemptLockLocked(st, nodeID, req)
	if conflict {
		return nil, -int32(syscall.EAGAIN)
	}
	return nil, errno
}

// attemptLockLocked applies req unless it conflicts. A canceled or closed request never acquires.
func (s *Server) attemptLockLocked(st *fuseRequestState, nodeID uint64, req fileLockEntry) (int32, bool) {
	if req.typ != syscall.F_UNLCK && req.typ != syscall.F_RDLCK && req.typ != syscall.F_WRLCK {
		return -int32(syscall.EINVAL), false
	}
	if req.typ != syscall.F_UNLCK {
		if s.closed.Load() || st.isCanceled() {
			return -int32(syscall.EINTR), false
		}
		for _, existing := range s.fileLocks[nodeID] {
			if locksConflict(req, existing) {
				return 0, true
			}
		}
	}
	applyLockChangeLocked(s, nodeID, req)
	s.lockCond.Broadcast()
	return 0, false
}

// startSetlkw runs on the reader. It answers immediately unless the request must wait, then
// hands the wait to a goroutine bounded by maxLockWaiters. Running out of waiters is ENOLCK,
// because a blocking lock call has no contract for EAGAIN.
func (s *Server) startSetlkw(st *fuseRequestState, req []byte) {
	nodeID := binary.LittleEndian.Uint64(req[16:24])
	lock, ok := parseFuseLockIn(req[fuseHeaderInSize:])
	if !ok {
		s.finishRequest(st, -int32(syscall.EINVAL))
		return
	}
	s.lockMu.Lock()
	errno, conflict := s.attemptLockLocked(st, nodeID, lock)
	if conflict {
		if s.lockWaiterCount >= s.maxLockWaiters {
			errno, conflict = -int32(syscall.ENOLCK), false
		} else {
			s.lockWaiterCount++
		}
	}
	s.lockMu.Unlock()
	if !conflict {
		s.finishRequest(st, errno)
		return
	}
	s.notifyLockWait(st.unique, lock, true)
	s.wg.Add(1)
	go s.waitForLock(st, nodeID, lock)
}

func (s *Server) waitForLock(st *fuseRequestState, nodeID uint64, lock fileLockEntry) {
	defer s.wg.Done()
	s.lockMu.Lock()
	errno, conflict := s.attemptLockLocked(st, nodeID, lock)
	for conflict {
		s.lockCond.Wait()
		errno, conflict = s.attemptLockLocked(st, nodeID, lock)
	}
	s.lockWaiterCount--
	s.lockMu.Unlock()
	s.notifyLockWait(st.unique, lock, false)
	s.finishRequest(st, errno)
}

func (s *Server) notifyLockWait(unique uint64, lock fileLockEntry, waiting bool) {
	if s.cfg.OnLockWait != nil {
		s.cfg.OnLockWait(unique, lock.start, lock.end, waiting)
	}
}

func (s *Server) finishRequest(st *fuseRequestState, errno int32) {
	_ = s.writeReply(st.unique, errno, nil)
	s.unregisterRequest(st.unique)
}

func applyLockChangeLocked(s *Server, nodeID uint64, req fileLockEntry) {
	existing := s.fileLocks[nodeID]
	if req.isFlock {
		kept := make([]fileLockEntry, 0, len(existing)+1)
		for _, lk := range existing {
			if lk.isFlock && lk.owner == req.owner {
				continue
			}
			kept = append(kept, lk)
		}
		if req.typ != syscall.F_UNLCK {
			kept = append(kept, req)
		}
		if len(kept) == 0 {
			delete(s.fileLocks, nodeID)
		} else {
			s.fileLocks[nodeID] = kept
		}
		return
	}

	others := make([]fileLockEntry, 0, len(existing))
	owned := make([]fileLockEntry, 0, len(existing)+2)
	for _, lk := range existing {
		if lk.isFlock || lk.owner != req.owner {
			others = append(others, lk)
			continue
		}
		if !rangesOverlap(req.start, req.end, lk.start, lk.end) {
			owned = append(owned, lk)
			continue
		}
		if lk.start < req.start {
			left := lk
			left.end = req.start - 1
			owned = append(owned, left)
		}
		if lk.end > req.end {
			right := lk
			right.start = req.end + 1
			owned = append(owned, right)
		}
	}
	if req.typ != syscall.F_UNLCK {
		owned = append(owned, req)
	}
	coalesced := coalescePOSIXRanges(owned)
	combined := append(others, coalesced...)
	if len(combined) == 0 {
		delete(s.fileLocks, nodeID)
	} else {
		s.fileLocks[nodeID] = combined
	}
}

func coalescePOSIXRanges(intervals []fileLockEntry) []fileLockEntry {
	if len(intervals) <= 1 {
		return intervals
	}
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].start != intervals[j].start {
			return intervals[i].start < intervals[j].start
		}
		return intervals[i].end < intervals[j].end
	})
	merged := make([]fileLockEntry, 0, len(intervals))
	for _, cur := range intervals {
		if len(merged) == 0 {
			merged = append(merged, cur)
			continue
		}
		last := &merged[len(merged)-1]
		adjacent := last.end < ^uint64(0) && last.end+1 == cur.start
		if last.typ == cur.typ && (cur.start <= last.end || adjacent) {
			if cur.end > last.end {
				last.end = cur.end
			}
			last.pid = cur.pid
			last.fh = cur.fh
			continue
		}
		merged = append(merged, cur)
	}
	return merged
}

func releasePOSIXLocksForOwner(s *Server, nodeID, handleNodeID, lockOwner uint64) {
	if lockOwner == 0 {
		return
	}
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	changed := filterNodeLocksLocked(s, nodeID, func(lk fileLockEntry) bool {
		return !lk.isFlock && lk.owner == lockOwner
	})
	if handleNodeID != 0 && handleNodeID != nodeID {
		if filterNodeLocksLocked(s, handleNodeID, func(lk fileLockEntry) bool {
			return !lk.isFlock && lk.owner == lockOwner
		}) {
			changed = true
		}
	}
	if changed {
		s.lockCond.Broadcast()
	}
}

func releaseLocksForHandle(s *Server, fh, nodeID uint64, releaseFlags uint32, lockOwner uint64) {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	shouldDrop := func(lk fileLockEntry) bool {
		if lk.fh == fh {
			return true
		}
		if lk.isFlock && releaseFlags&fuseReleaseFlockUnlock != 0 && lockOwner != 0 && lk.owner == lockOwner {
			return true
		}
		if !lk.isFlock && releaseFlags&fuseReleaseFlush != 0 && lockOwner != 0 && lk.owner == lockOwner {
			return true
		}
		return false
	}
	changed := false
	if nodeID != 0 {
		changed = filterNodeLocksLocked(s, nodeID, shouldDrop)
	} else {
		for nid := range s.fileLocks {
			if filterNodeLocksLocked(s, nid, shouldDrop) {
				changed = true
			}
		}
	}
	if changed {
		s.lockCond.Broadcast()
	}
}

func filterNodeLocksLocked(s *Server, nodeID uint64, drop func(fileLockEntry) bool) bool {
	locks := s.fileLocks[nodeID]
	if len(locks) == 0 {
		return false
	}
	kept := locks[:0]
	removed := false
	for _, lk := range locks {
		if drop(lk) {
			removed = true
			continue
		}
		kept = append(kept, lk)
	}
	if len(kept) == 0 {
		delete(s.fileLocks, nodeID)
	} else {
		s.fileLocks[nodeID] = kept
	}
	return removed
}
