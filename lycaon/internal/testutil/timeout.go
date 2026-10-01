package testutil

import (
	"time"

	"github.com/lycaon/lycaon/internal/runnerbudget"
)

// Timeout scales scheduler-dependent test waits.
func Timeout(base time.Duration) time.Duration {
	return base * time.Duration(runnerbudget.TimeoutScale())
}
