package pythonruntime

import (
	"fmt"
	"sync/atomic"
)

// WorkMeter accounts for deterministic guest work (via Wasm loop+function fuel
// instrumentation), host work, and staged bytes against per-invocation limits,
// latching StopWorkLimitExceeded before overflowing host budgets.
type WorkMeter struct {
	maxGuestWork   uint64
	maxHostWork    uint64
	maxStagedBytes int64
	guestWork      atomic.Uint64
	hostWork       atomic.Uint64
	stagedBytes    atomic.Int64
	latch          *StopLatch
}

// NewWorkMeter creates a WorkMeter enforcing maxGuestWork, maxHostWork, and
// maxStagedBytes (0 means unbounded for that dimension).
func NewWorkMeter(maxGuestWork, maxHostWork uint64, maxStagedBytes int64, latch *StopLatch) *WorkMeter {
	return &WorkMeter{
		maxGuestWork:   maxGuestWork,
		maxHostWork:    maxHostWork,
		maxStagedBytes: maxStagedBytes,
		latch:          latch,
	}
}

// ReserveHostWork checks and reserves host work units BEFORE performing a host
// operation. Returns false and latches StopWorkLimitExceeded if maxHostWork
// would be exceeded.
func (m *WorkMeter) ReserveHostWork(units uint64, operationID string) bool {
	if m == nil || units == 0 {
		return true
	}
	for {
		cur := m.hostWork.Load()
		next := cur + units
		if m.maxHostWork > 0 && next > m.maxHostWork {
			if m.latch != nil {
				m.latch.Trigger(TerminalStop{
					Code:        StopWorkLimitExceeded,
					OperationID: operationID,
					Reason:      fmt.Sprintf("host work %d would exceed limit %d", next, m.maxHostWork),
				})
			}
			return false
		}
		if m.hostWork.CompareAndSwap(cur, next) {
			return true
		}
	}
}

// ReserveStagedBytes checks and reserves staged byte capacity BEFORE writing
// to a workspace overlay descriptor. Returns false and latches
// StopWorkLimitExceeded if maxStagedBytes would be exceeded.
func (m *WorkMeter) ReserveStagedBytes(delta int64, operationID string) bool {
	if m == nil || delta <= 0 {
		return true
	}
	for {
		cur := m.stagedBytes.Load()
		next := cur + delta
		if m.maxStagedBytes > 0 && next > m.maxStagedBytes {
			if m.latch != nil {
				m.latch.Trigger(TerminalStop{
					Code:        StopWorkLimitExceeded,
					OperationID: operationID,
					Reason:      fmt.Sprintf("staged bytes %d would exceed limit %d", next, m.maxStagedBytes),
				})
			}
			return false
		}
		if m.stagedBytes.CompareAndSwap(cur, next) {
			return true
		}
	}
}

// SetGuestWork records final guest fuel consumed from the instrumented Wasm
// module.
func (m *WorkMeter) SetGuestWork(units uint64) {
	if m == nil {
		return
	}
	m.guestWork.Store(units)
}

// GuestWork returns total charged guest work units.
func (m *WorkMeter) GuestWork() uint64 {
	if m == nil {
		return 0
	}
	return m.guestWork.Load()
}

// HostWork returns total charged host work units.
func (m *WorkMeter) HostWork() uint64 {
	if m == nil {
		return 0
	}
	return m.hostWork.Load()
}

// StagedBytes returns total reserved staged bytes.
func (m *WorkMeter) StagedBytes() int64 {
	if m == nil {
		return 0
	}
	return m.stagedBytes.Load()
}
