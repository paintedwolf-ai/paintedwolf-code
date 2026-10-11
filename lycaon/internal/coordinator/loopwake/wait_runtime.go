package loopwake

import (
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
)

const (
	defaultCoordinatorWaitInterval = 2 * time.Minute
	// defaultWorkflowObligationInterval paces unmet workflow obligations.
	defaultWorkflowObligationInterval = 2 * time.Minute
)

type sessionSleep struct {
	mu               sync.Mutex
	armed            bool
	untilComplete    bool
	until            time.Time
	interruptedUntil time.Time
	reason           string
	timer            *time.Timer
	timerDone        chan struct{}
	timerGeneration  uint64
	waitThisTurn     bool
	waitTriggers     []WaitTrigger
	processHandles   []string
	workerHandles    []string
	// mover identifies who may end the sleep.
	mover SleepMover
	// activityID is non-empty exactly while an awaiting_wake lease is open.
	activityID        string
	activityStartedAt time.Time
}
type UserTurnContinuation uint8

const (
	// UserTurnSettled means no host continuation remains.
	UserTurnSettled UserTurnContinuation = iota
	// UserTurnContinues keeps the visible turn open.
	UserTurnContinues
)

func alwaysBreaksSleep(wake anchor.ID, completingJobID string) bool {
	if strings.TrimSpace(completingJobID) != "" {
		return true
	}
	switch wake {
	case anchor.LegFinished, anchor.WorkerTaskFinished, anchor.WorkerBudgetRequested:
		return true
	default:
		return false
	}
}

type sleepArm struct {
	until          time.Time
	untilComplete  bool
	reason         string
	triggers       []WaitTrigger
	processHandles []string
	workerHandles  []string
	mover          SleepMover
}

func cancelSleepTimerLocked(st *sessionSleep) {
	st.timerGeneration++
	if st.timer != nil {
		if st.timer.Stop() && st.timerDone != nil {
			close(st.timerDone)
		}
		st.timer = nil
		st.timerDone = nil
	}
}
func sleepArmedLocked(st *sessionSleep, now time.Time) bool {
	if st == nil || !st.armed {
		return false
	}
	if _, hasTimer := waitTriggerSet(st.waitTriggers)[WaitTriggerTimer]; !hasTimer {
		return true
	}
	return now.Before(st.until)
}
func CapWaitDuration(requested, maxSleep time.Duration) time.Duration {
	if requested <= 0 {
		requested = DefaultWaitSeconds * time.Second
	}
	minimum := MinWaitSeconds * time.Second
	if requested < minimum {
		requested = minimum
	}
	d := requested
	if maxSleep > 0 && d > maxSleep {
		return maxSleep
	}
	return d
}
