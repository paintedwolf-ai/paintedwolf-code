// Package budget meters the selected bounded detector entry point.
package budget

import (
	"context"
	"fmt"
)

// Limits bound cumulative allocation reservations and candidate enumeration.
// These are logical backing-store/work reservations, not a Go heap ceiling.
type Limits struct {
	MaxInputBytes, MaxAllocationBytes, MaxWork int64
	MaxMatches                                 int
	// Reserve admits each allocation against a caller-owned aggregate pool.
	Reserve func(int64) error
	// Release returns only a reserved envelope proven not to have been allocated.
	Release func(int64)
}

// Error reports an exhausted resource without treating partial matches as safe.
type Error struct {
	Resource          string
	Maximum, Observed int64
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: requested %d, maximum %d", e.Resource, e.Observed, e.Maximum)
}

type abort struct{ err error }

// Meter is invocation-local and is never stored on a shared Detector.
type Meter struct {
	Context         context.Context
	Limits          Limits
	allocated, work int64
}

func New(ctx context.Context, limits Limits) *Meter { return &Meter{Context: ctx, Limits: limits} }

// Recover converts only this package's private refusal into a terminal error.
func Recover(err *error) {
	if v := recover(); v != nil {
		if a, ok := v.(abort); ok {
			*err = a.err
		} else {
			panic(v)
		}
	}
}
func (m *Meter) Check() {
	if m != nil {
		if err := m.Context.Err(); err != nil {
			panic(abort{err})
		}
	}
}
func (m *Meter) Refuse(resource string, maximum, observed int64) {
	panic(abort{&Error{resource, maximum, observed}})
}

// Allocate charges before a selected operation may allocate backing storage.
// Cumulative charging includes overlapping old/new buffers and abandoned results.
func (m *Meter) Allocate(n int64) {
	if m == nil {
		return
	}
	m.Check()
	if n < 0 || n > m.Limits.MaxAllocationBytes-m.allocated {
		m.Refuse("allocation_bytes", m.Limits.MaxAllocationBytes, m.allocated+n)
	}
	if m.Limits.Reserve != nil {
		if err := m.Limits.Reserve(n); err != nil {
			panic(abort{err})
		}
	}
	m.allocated += n
}
func (m *Meter) Work(n int64) {
	if m == nil {
		return
	}
	m.Check()
	if n < 0 || n > m.Limits.MaxWork-m.work {
		m.Refuse("work", m.Limits.MaxWork, m.work+n)
	}
	m.work += n
}

// enumeration reserves a sentinel envelope before FindAll, then returns only
// the unused reservation after the actual result capacity is conservatively
// charged. Actual backing allocations remain cumulatively charged.
func (m *Meter) enumeration(inputBytes, width int) (int, func(int)) {
	if m == nil {
		return -1, func(int) {}
	}
	if width < 256 {
		width = 256
	}
	n := min(m.Limits.MaxMatches+1, inputBytes+1)
	slots := (m.Limits.MaxAllocationBytes - m.allocated - 512) / int64(width)
	if slots < 1 {
		m.Refuse("allocation_bytes", m.Limits.MaxAllocationBytes, m.Limits.MaxAllocationBytes+1)
	}
	n = min(n, int(slots))
	reservation := 512 + int64(n)*int64(width)
	m.Work(int64(inputBytes) + int64(n))
	m.Allocate(reservation)
	return n, func(actual int) {
		m.Count(actual)
		if actual == n && n < inputBytes+1 {
			m.Refuse("allocation_bytes", m.Limits.MaxAllocationBytes, m.Limits.MaxAllocationBytes+1)
		}
		unused := int64(n-actual) * int64(width)
		m.allocated -= unused
		if m.Limits.Release != nil {
			m.Limits.Release(unused)
		}
	}
}
func (m *Meter) Count(n int) {
	if m != nil && n > m.Limits.MaxMatches {
		m.Refuse("match_candidates", int64(m.Limits.MaxMatches), int64(n))
	}
}
