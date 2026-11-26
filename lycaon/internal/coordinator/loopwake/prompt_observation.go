package loopwake

import "github.com/lycaon/lycaon/internal/actionoutcome"

type observedWorkflow struct {
	turn     *actionoutcome.Turn
	runID    string
	revision int64
}
