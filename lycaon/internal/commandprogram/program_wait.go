package commandprogram

import (
	"context"
	"time"
)

// WaitState is what a program waits on while every live statement and job is
// blocked: a timer, exact job handles, or both. The evaluator derives it from
// its own and supervision state, never from the program's text.
type WaitState struct {
	process bool
	// Statement is the waiting statement's source text; it names the wait.
	Statement string
	// Until is the earliest timer wake. Zero means only jobs can wake it.
	Until time.Time
	// Jobs are the session job handles whose settlement wakes the program.
	Jobs []string
}

// ProgramWaits receives a program's wait states. Block records that the
// program is blocked until the returned unblock runs. It refuses, before the
// program blocks, a wait that cannot complete within the program's bound.
type ProgramWaits interface {
	Block(ctx context.Context, state WaitState) (unblock func(), err error)
}

// Clock is the time source program waits are computed on.
type Clock interface {
	Now() time.Time
	// AfterFunc runs f once d has passed; stop cancels it and reports whether
	// it did so before f ran.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

// SystemClock is the host's wall clock.
func SystemClock() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}
