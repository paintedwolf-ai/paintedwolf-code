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
	report := func(ctx context.Context, program string) bool {
		// Committed state is published even when the transition's own context ended.
		receipt, err := recorder.ActivitySummary(context.WithoutCancel(ctx), tc.Identity.SessionID, program)
		if err != nil {
			return false
		}
		tc.Effects.ReportProgress(api.ToolProgress{Phase: "executing", CommandProgram: &receipt})
		return true
	}
	return invocation.WithActivityObserver(ctx, invocation.ActivityObserver{
		Execution: func(ctx context.Context, program, execution string) {
			mu.Lock()
			defer mu.Unlock()
			// Repetitions coalesce; new structural states publish immediately.
			transition, err := recorder.ActivityTransition(context.WithoutCancel(ctx), execution)
			if err != nil {
				return
			}
			if known[transition] && time.Since(sampled) < time.Second {
				return
			}
			if report(ctx, program) {
				known[transition] = true
				sampled = time.Now()
			}
		},
		Settled: func(ctx context.Context, program string) {
			mu.Lock()
			defer mu.Unlock()
			report(ctx, program)
		},
	})
}
