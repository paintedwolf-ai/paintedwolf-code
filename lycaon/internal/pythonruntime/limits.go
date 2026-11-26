package pythonruntime

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

type workMeterContextKey struct{}

// WorkMeter accounts for deterministic guest work (Wasm function calls) and
// host work (charged via OperationObserver / WorkspaceSysFS) against per-
// invocation limits, latching StopWorkLimitExceeded on overflow.
type WorkMeter struct {
	maxGuestWork uint64
	maxHostWork  uint64
	guestWork    atomic.Uint64
	hostWork     atomic.Uint64
	latch        *StopLatch
}

// NewWorkMeter creates a WorkMeter enforcing maxGuestWork and maxHostWork
// (0 means unbounded for that dimension).
func NewWorkMeter(maxGuestWork, maxHostWork uint64, latch *StopLatch) *WorkMeter {
	return &WorkMeter{
		maxGuestWork: maxGuestWork,
		maxHostWork:  maxHostWork,
		latch:        latch,
	}
}

// ChargeGuestWork adds units to guest work and trips StopWorkLimitExceeded if
// maxGuestWork is exceeded. Returns false once the limit is exceeded.
func (m *WorkMeter) ChargeGuestWork(units uint64) bool {
	if m == nil || units == 0 {
		return true
	}
	total := m.guestWork.Add(units)
	if m.maxGuestWork > 0 && total > m.maxGuestWork {
		if m.latch != nil {
			m.latch.Trigger(TerminalStop{
				Code:   StopWorkLimitExceeded,
				Reason: fmt.Sprintf("guest work %d exceeded limit %d", total, m.maxGuestWork),
			})
		}
		return false
	}
	return true
}

// ChargeHostWork adds units to host work and trips StopWorkLimitExceeded if
// maxHostWork is exceeded. Returns false once the limit is exceeded.
func (m *WorkMeter) ChargeHostWork(units uint64, operationID string) bool {
	if m == nil || units == 0 {
		return true
	}
	total := m.hostWork.Add(units)
	if m.maxHostWork > 0 && total > m.maxHostWork {
		if m.latch != nil {
			m.latch.Trigger(TerminalStop{
				Code:        StopWorkLimitExceeded,
				OperationID: operationID,
				Reason:      fmt.Sprintf("host work %d exceeded limit %d", total, m.maxHostWork),
			})
		}
		return false
	}
	return true
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

// WithWorkMeter attaches meter to ctx for per-invocation guest work metering.
func WithWorkMeter(ctx context.Context, meter *WorkMeter) context.Context {
	if meter == nil {
		return ctx
	}
	return context.WithValue(ctx, workMeterContextKey{}, meter)
}

// WorkMeterFromContext retrieves the per-invocation WorkMeter from ctx, if any.
func WorkMeterFromContext(ctx context.Context) *WorkMeter {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(workMeterContextKey{}).(*WorkMeter)
	return m
}

type workMeterListener struct{}

func newWorkMeterListenerFactory() experimental.FunctionListenerFactory {
	l := &workMeterListener{}
	return experimental.FunctionListenerFactoryFunc(func(_ api.FunctionDefinition) experimental.FunctionListener {
		return l
	})
}

func (l *workMeterListener) Before(ctx context.Context, _ api.Module, _ api.FunctionDefinition, _ []uint64, _ experimental.StackIterator) {
	if meter := WorkMeterFromContext(ctx); meter != nil {
		meter.ChargeGuestWork(1)
	}
}

func (l *workMeterListener) After(context.Context, api.Module, api.FunctionDefinition, []uint64) {}

func (l *workMeterListener) Abort(context.Context, api.Module, api.FunctionDefinition, error) {}
