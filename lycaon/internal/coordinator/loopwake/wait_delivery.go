package loopwake

import (
	"strings"
	"sync/atomic"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
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

func processWakeReport(wake anchor.ID, env anchor.Envelope) string {
	switch wake {
	case anchor.ProcessFinished:
		return strings.TrimSpace(env.CommandCompletionDigest)
	case anchor.ProcessRefused:
		return strings.TrimSpace(env.CommandRefusalDigest)
	default:
		return ""
	}
}
