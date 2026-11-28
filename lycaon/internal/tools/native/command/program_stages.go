package command

import (
	"context"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
)

// stageSettler records a stage's final reports. Only its first call counts.
type stageSettler func(confine.SandboxRefusals, []confine.EgressHost)

// stageTracker counts a program's external stages toward the background job
// that carries it. Until a root program is continued as a job, its stages are
// held here and handed to the job when it starts.
type stageTracker struct {
	mu      sync.Mutex
	attach  func(bgprocess.ProgramStage) stageSettler
	held    []*heldStage
	history bgprocess.ProgramHistory
}

type heldStage struct {
	stage  bgprocess.ProgramStage
	settle stageSettler
}

type stageTrackerKey struct{}

func withStageTracker(ctx context.Context, tracker *stageTracker) context.Context {
	return context.WithValue(ctx, stageTrackerKey{}, tracker)
}

// boundStageTracker routes every stage straight to a running job.
func boundStageTracker(attach func(bgprocess.ProgramStage) stageSettler) *stageTracker {
	return &stageTracker{attach: attach}
}

// attachProgramStage counts an external stage toward the program job that
// owns ctx, if any.
func attachProgramStage(ctx context.Context, stage bgprocess.ProgramStage) stageSettler {
	t, _ := ctx.Value(stageTrackerKey{}).(*stageTracker)
	if t == nil {
		return func(confine.SandboxRefusals, []confine.EgressHost) {}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.attach != nil {
		return t.attach(stage)
	}
	held := &heldStage{stage: stage}
	t.held = append(t.held, held)
	return t.settleHeld(held)
}

func (t *stageTracker) settleHeld(held *heldStage) stageSettler {
	return func(refusals confine.SandboxRefusals, network []confine.EgressHost) {
		t.mu.Lock()
		defer t.mu.Unlock()
		if held.settle != nil {
			held.settle(refusals, network)
			return
		}
		i := slices.Index(t.held, held)
		if i < 0 {
			return
		}
		t.held = slices.Delete(t.held, i, i+1)
		t.history.Settle(held.stage, refusals, network)
	}
}

// bind starts the program's job from what its stages gathered so far, then
// hands running stages to it. Stages launching meanwhile wait on the lock and
// attach to the job.
func (t *stageTracker) bind(
	start func(bgprocess.ProgramHistory) (string, error),
	attach func(handle string) func(bgprocess.ProgramStage) stageSettler,
) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	handle, err := start(t.history)
	if err != nil {
		return "", err
	}
	t.attach = attach(handle)
	for _, held := range t.held {
		held.settle = t.attach(held.stage)
	}
	t.held = nil
	return handle, nil
}
