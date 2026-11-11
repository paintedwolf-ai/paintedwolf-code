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
	known := map[string]bool{}
	return invocation.WithActivityObserver(ctx, func(ctx context.Context, program, execution string) {
		mu.Lock()
		defer mu.Unlock()
		// Repetitions coalesce; new structural states publish immediately.
		transition, err := recorder.ActivityTransition(ctx, execution)
		if err != nil {
			return
		}
		if known[transition] && time.Since(sampled) < time.Second {
			return
		}
		receipt, err := recorder.ActivitySummary(ctx, tc.Identity.SessionID, program)
		if err != nil {
			return
		}
		known[transition] = true
		sampled = time.Now()
		tc.Effects.ReportProgress(api.ToolProgress{Phase: "executing", CommandProgram: &receipt})
	})
}
