package loopwake

import (
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"sync/atomic"
	"time"
)

const (
	waitResumeRetryInitial = time.Second
	waitResumeRetryMaximum = 30 * time.Second
)

// WaitDelivery binds one settled lease to its admission acknowledgement.
type WaitDelivery struct {
	LeaseID   string
	Condition awaitstore.Condition
	Pending   func() bool
	Admitted  func() error
}

type waitWinner struct {
	LeaseID        string
	Condition      awaitstore.Condition
	deliveryActive atomic.Bool
	retryScheduled atomic.Bool
	retryAttempt   atomic.Uint32
}

func waitResumeRetryDelay(attempt uint32) time.Duration {
	delay := waitResumeRetryInitial
	for step := uint32(1); step < attempt && delay < waitResumeRetryMaximum; step++ {
		delay *= 2
		if delay >= waitResumeRetryMaximum {
			return waitResumeRetryMaximum
		}
	}
	return delay
}
