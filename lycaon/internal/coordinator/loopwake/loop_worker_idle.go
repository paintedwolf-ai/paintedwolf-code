package loopwake

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerCycleIdle excludes the job whose terminal summary is landing.
type WorkerCycleIdle func(ctx context.Context, sess *api.Session, completingJobID string) (bool, error)

// Terminal acknowledgement satisfies subscribed waits, including on cancellation.
