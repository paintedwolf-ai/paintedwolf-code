package coordinator_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

// Concurrent worker turns share one runtime and dependency lock.
func TestRuntimeConcurrentPromptTurnSafe(t *testing.T) {
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{
		LoopDeps: func() promptloop.PromptLoopDeps {
			return promptloop.PromptLoopDeps{
				Context: promptloop.ContextDeps{
					Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
					BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
						return history, nil
					},
				},
			}
		},
		AssemblyDeps: func() assembly.AssemblyDeps {
			return assembly.AssemblyDeps{}
		},
		LoopWakeDeps: func() loopwake.LoopDeps {
			return loopwake.LoopDeps{}
		},
	})
	ctx := context.Background()
	history := []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sid := fmt.Sprintf("sess-%d", n)
			rt.BeginPromptTurn(sid, "kick")
			if _, err := rt.BuildCompletionMessages(ctx, &api.Session{ID: sid}, history, nil); err != nil {
				t.Errorf("BuildCompletionMessages(%s): %v", sid, err)
			}
			rt.EndPromptTurn(sid)
			rt.DrainLoopPending(ctx, sid)
		}(i)
	}
	wg.Wait()
}
