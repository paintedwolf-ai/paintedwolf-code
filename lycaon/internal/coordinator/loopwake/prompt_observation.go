package loopwake

import "github.com/lycaon/lycaon/internal/actionoutcome"

type observedWorkflow struct {
	sequence uint64
	turn     *actionoutcome.Turn
	runID    string
	revision int64
}
