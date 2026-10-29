package commandsurface

import (
	"context"
	"fmt"
	"sync"
)

// ProgramStateAdmission refuses an aggregate state allocation before insertion.
// It must not wait for this same program to relinquish memory to make room.
type ProgramStateAdmission interface {
	ReserveProgramState(context.Context, int64) (release func(), err error)
}

// ProgramSnapshotLease owns one detached execution snapshot's accounting.
// Keep the lease until every consumer has released the snapshot and its strings.
type ProgramSnapshotLease struct {
	Plan    ProgramSnapshot
	release func()
	once    sync.Once
}

func (s *ProgramSnapshotLease) Close() error {
	s.once.Do(func() { s.Plan = ProgramSnapshot{}; s.release() })
	return nil
}

func (p *ParsedProgram) SnapshotWithMemory(ctx context.Context, admission ProgramStateAdmission) (*ProgramSnapshotLease, error) {
	if ctx == nil || admission == nil {
		return nil, fmt.Errorf("execution snapshot requires context and state admission")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.RLock()
	closed, bound := p.closed, p.stateBytes
	p.mu.RUnlock()
	if closed {
		return nil, fmt.Errorf("parsed program is closed")
	}
	release, err := admission.ReserveProgramState(ctx, bound)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("snapshot state admission returned no release")
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	if p.closed {
		release()
		return nil, fmt.Errorf("parsed program is closed")
	}
	return &ProgramSnapshotLease{Plan: cloneProgram(p.plan), release: release}, nil
}

// Close relinquishes the captured plan's state. Detached execution snapshots
// retain their own leases; parser scratch is released independently.
func (p *ParsedProgram) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		p.plan = ProgramSnapshot{}
		p.source = nil
		if p.releaseState != nil {
			p.releaseState()
		}
	}
	return nil
}
