package exec

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var errCommandDeadline = errors.New("command deadline exceeded")

// TimeoutError records the effective deadline and measured execution time,
// including when a caller's deadline is earlier than the command's limit.
type TimeoutError struct {
	Elapsed  time.Duration
	Deadline time.Time
	Cause    error
	// CommandDeadline distinguishes the command limit from an inherited deadline.
	CommandDeadline bool
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%v after %v", ErrTimeout, e.Elapsed.Round(time.Millisecond))
}

func (e *TimeoutError) Unwrap() []error {
	causes := []error{ErrTimeout, context.DeadlineExceeded}
	if e.Cause != nil {
		causes = append(causes, e.Cause)
	}
	return causes
}

func commandTimeout(ctx context.Context, started time.Time) *TimeoutError {
	deadline, _ := ctx.Deadline()
	cause := context.Cause(ctx)
	return &TimeoutError{
		Elapsed: time.Since(started), Deadline: deadline, Cause: cause,
		CommandDeadline: errors.Is(cause, errCommandDeadline),
	}
}
