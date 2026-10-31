package tools

import (
	"context"
	"errors"
	"time"
)

// ProgramParking ends a command call's turn while its waiting program keeps
// running under supervision. The session's wait lease follows the program,
// and the program's own end resumes the turn with its complete result.
type ProgramParking interface {
	ParkProgram(ctx context.Context, tc ToolContext, park ProgramPark) (ParkedProgram, error)
}

// ErrParkUnavailable means the session cannot hold another parked call; the
// caller keeps its program in the foreground.
var ErrParkUnavailable = errors.New("session cannot park another call")

// ErrWaitEnded means the wait that asked to park ended before the park took
// it; the program runs on in the foreground.
var ErrWaitEnded = errors.New("program wait ended before it parked")

// ProgramPark is one waiting program to park.
type ProgramPark struct {
	ProgramID string
	// Reason is the waiting statement, e.g. `sleep 30` or `wait %1`.
	Reason string
	// Deadline is the program's outer bound.
	Deadline time.Time
	// Claim takes the park if the program still waits where it asked to park.
	// The park arms nothing once Claim refuses.
	Claim func() bool
	// Interrupt stops the program at its waiting statement and returns its
	// result, interrupted or, when it ended first, complete.
	Interrupt func(context.Context) string
}

// ParkedProgram is the lease a parked program resumes its turn through.
type ParkedProgram interface {
	LeaseID() string
	// Settle delivers the program's end: its result, or the host error that
	// kept one from being produced.
	Settle(ctx context.Context, result string, err error)
}
