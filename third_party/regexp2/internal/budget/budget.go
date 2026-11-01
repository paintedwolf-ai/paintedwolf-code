package budget

import (
	"context"
	"fmt"
)

type Limits struct {
	MaxBytes, MaxSteps int64
	Reserve            func(context.Context, int64) (func(), error)
}
type Error struct {
	Resource          string
	Maximum, Observed int64
}

func (e *Error) Error() string {
	return fmt.Sprintf("regexp2 bounded %s: maximum %d observed %d", e.Resource, e.Maximum, e.Observed)
}

type Abort struct{ Err error }
type Meter struct {
	ctx          context.Context
	limits       Limits
	bytes, steps int64
	releases     []func()
}

func New(ctx context.Context, limits Limits) *Meter {
	if ctx == nil || limits.MaxBytes < 1 || limits.MaxSteps < 1 {
		panic(Abort{fmt.Errorf("bounded regex requires context and positive limits")})
	}
	if limits.MaxBytes < 512 {
		panic(Abort{&Error{"bytes", limits.MaxBytes, 512}})
	}
	var release func()
	if limits.Reserve != nil {
		var err error
		release, err = limits.Reserve(ctx, 512)
		if err == nil && release == nil {
			err = fmt.Errorf("bounded regex admission returned no lease")
		}
		if err != nil {
			panic(Abort{err})
		}
	}
	m := &Meter{ctx: ctx, limits: limits, bytes: 512}
	if release != nil {
		m.releases = []func(){release}
	}
	return m
}
func (m *Meter) Check() {
	if m == nil {
		return
	}
	if err := m.ctx.Err(); err != nil {
		panic(Abort{err})
	}
}
func (m *Meter) Step(n int64) {
	if m == nil {
		return
	}
	m.Check()
	if n < 0 || n > m.limits.MaxSteps-m.steps {
		panic(Abort{&Error{"steps", m.limits.MaxSteps, m.steps + n}})
	}
	m.steps += n
}

// Alloc reserves cumulative allocation payload including old/new growth overlap.
// Runtime metadata/allocator rounding and source-owned bytes are not heap claims.
func (m *Meter) Alloc(n int64) {
	if m == nil {
		return
	}
	m.Check()
	n += 32
	if n < 0 || n > m.limits.MaxBytes-m.bytes {
		panic(Abort{&Error{"bytes", m.limits.MaxBytes, m.bytes + n}})
	}
	if m.limits.Reserve != nil {
		release, err := m.limits.Reserve(m.ctx, n)
		if err == nil && release == nil {
			err = fmt.Errorf("bounded regex admission returned no lease")
		}
		if err != nil {
			panic(Abort{err})
		}
		m.releases = append(m.releases, release)
	}
	m.bytes += n
}

// Steps reports the work admitted so far.
func (m *Meter) Steps() int64 {
	if m == nil {
		return 0
	}
	return m.steps
}

func (m *Meter) Close() {
	for i := len(m.releases) - 1; i >= 0; i-- {
		m.releases[i]()
	}
	m.releases = nil
}
