package toolcontract

import (
	"context"
	"fmt"
	"sync"
)

// ProgramMemoryLimits sizes one program's aggregate admission accounts.
// Working covers parser scratch, predicate/pattern work and stream codec
// scratch; State covers retained source, plans, snapshots and projections.
type ProgramMemoryLimits struct {
	Working int64
	State   int64
}

// CatalogProgramMemoryLimits derives the accounts from the frozen catalog:
// evaluation_memory_bytes for working storage, program_state_bytes for state.
func CatalogProgramMemoryLimits() (ProgramMemoryLimits, error) {
	catalog, err := operationLanguage()
	if err != nil {
		return ProgramMemoryLimits{}, err
	}
	var limits ProgramMemoryLimits
	for _, limit := range catalog.Language.Limits {
		switch limit.ID {
		case "evaluation_memory_bytes":
			limits.Working = limit.Maximum
		case "program_state_bytes":
			limits.State = limit.Maximum
		}
	}
	if limits.Working < 1 || limits.State < 1 {
		return limits, fmt.Errorf("compiled language is missing program memory bounds")
	}
	return limits, nil
}

// ProgramMemoryLimitError is a typed refusal; admission never waits for this
// program's own leases to be released.
type ProgramMemoryLimitError struct {
	Account           string
	Maximum, Observed int64
}

func (e *ProgramMemoryLimitError) Error() string {
	return fmt.Sprintf("program %s memory exceeded: %d > %d", e.Account, e.Observed, e.Maximum)
}

// ProgramMemory is the one aggregate memory admission shared by a program's
// parser, snapshots, predicates, streams and native dispatch. It is logical
// capacity accounting, not a heap or RSS limit, and grants no authority.
type ProgramMemory struct {
	mu           sync.Mutex
	limits       ProgramMemoryLimits
	working      int64
	state        int64
	peak         ProgramMemoryUsage
	reservations int64
}

// ProgramMemoryUsage reports currently held (or peak) bytes per account.
type ProgramMemoryUsage struct {
	Working int64
	State   int64
}

func NewProgramMemory(limits ProgramMemoryLimits) (*ProgramMemory, error) {
	if limits.Working < 1 || limits.State < 1 {
		return nil, fmt.Errorf("program memory requires positive working and state limits")
	}
	return &ProgramMemory{limits: limits}, nil
}

// Reserve admits working storage; release returns it exactly once.
func (m *ProgramMemory) Reserve(ctx context.Context, n int64) (func(), error) {
	return m.reserve(ctx, n, false)
}

// ReserveProgramState admits retained state; release returns it exactly once.
func (m *ProgramMemory) ReserveProgramState(ctx context.Context, n int64) (func(), error) {
	return m.reserve(ctx, n, true)
}

// ReserveBytes admits secret-screening working bytes, which the detector
// returns by count with ReleaseBytes rather than through a lease.
func (m *ProgramMemory) ReserveBytes(ctx context.Context, n int64) error {
	_, err := m.reserve(ctx, n, false)
	return err
}

// ReleaseBytes returns working bytes admitted by ReserveBytes.
func (m *ProgramMemory) ReleaseBytes(n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.working = max(m.working-n, 0)
}

// InUse reports currently held bytes.
func (m *ProgramMemory) InUse() ProgramMemoryUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return ProgramMemoryUsage{Working: m.working, State: m.state}
}

// Reservations counts admitted reservations, including released ones.
func (m *ProgramMemory) Reservations() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reservations
}

// Peak reports the highest concurrently held bytes per account.
func (m *ProgramMemory) Peak() ProgramMemoryUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peak
}

func (m *ProgramMemory) reserve(ctx context.Context, n int64, state bool) (func(), error) {
	if m == nil || ctx == nil {
		return nil, fmt.Errorf("program memory admission requires a ledger and context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n < 0 {
		return nil, fmt.Errorf("program memory reservation must be nonnegative")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	used, maximum, account := &m.working, m.limits.Working, "working"
	if state {
		used, maximum, account = &m.state, m.limits.State, "state"
	}
	if n > maximum-*used {
		return nil, &ProgramMemoryLimitError{Account: account, Maximum: maximum, Observed: *used + n}
	}
	*used += n
	m.reservations++
	m.peak.Working, m.peak.State = max(m.peak.Working, m.working), max(m.peak.State, m.state)
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			*used -= n
			m.mu.Unlock()
		})
	}, nil
}
