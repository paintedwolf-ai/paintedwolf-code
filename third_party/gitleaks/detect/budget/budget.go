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

// Enumeration reserves the entire allowed backing-store envelope before FindAll.
// One extra candidate is reserved to distinguish truncation from completeness.
func (m *Meter) Enumeration(inputBytes, width int) int {
	if m == nil {
		return -1
	}
	n := m.Limits.MaxMatches + 1
	if inputBytes+1 < n {
		n = inputBytes + 1
	}
	m.Work(int64(inputBytes) + int64(n))
	if width < 128 {
		width = 128
	}
	m.Allocate(512 + int64(n)*int64(width))
	return n
}
func (m *Meter) Count(n int) {
	if m != nil && n > m.Limits.MaxMatches {
		m.Refuse("match_candidates", int64(m.Limits.MaxMatches), int64(n))
	}
}
