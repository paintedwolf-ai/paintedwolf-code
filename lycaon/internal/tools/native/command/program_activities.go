package command

import (
	"context"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func observeProgramActivities(ctx context.Context, recorder *invocation.SQLRecorder, tc tools.ToolContext) context.Context {
	if tc.Effects.ReportProgress == nil {
		return ctx
	}
	var mu sync.Mutex
	var sampled time.Time
	var previous *api.CommandProgramReceipt
	known := map[string]bool{}
	return invocation.WithActivityObserver(ctx, func(ctx context.Context, program, execution string) {
		mu.Lock()
		defer mu.Unlock()
		// Overflow counters coalesce; visible execution transitions publish immediately.
		if previous != nil && previous.ActivitiesOmitted > 0 && !known[execution] && time.Since(sampled) < time.Second {
			return
		}
		receipt, err := recorder.ActivitySummary(ctx, tc.Identity.SessionID, program)
		if err != nil {
			return
		}
		known = make(map[string]bool, len(receipt.ActivityExecutions))
		for _, child := range receipt.ActivityExecutions {
			known[child.ID] = true
		}
		sampled = time.Now()
		previous = &receipt
		tc.Effects.ReportProgress(api.ToolProgress{Phase: "executing", CommandProgram: &receipt})
	})
}
