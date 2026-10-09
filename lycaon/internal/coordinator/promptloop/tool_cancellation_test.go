package promptloop

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCanceledToolRunKeepsInterruptionFactsAndOutput(t *testing.T) {
	for _, tool := range []string{"read", "command", "http_request"} {
		t.Run(tool, func(t *testing.T) {
			contract, ok := toolcontract.Lookup(tool)
			if !ok {
				t.Fatalf("missing contract for %s", tool)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			loop := NewPromptLoopForTest(PromptLoopDeps{})
			run := toolInvocations{loop}.finalizeToolRun(ctx, &api.Session{ID: "session"}, completedToolRun{
				sessionID: "session", call: api.ToolCall{ID: "call", Name: tool}, contract: contract,
				toolCtx: tools.ToolContext{
					Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{OwnerInvoked: true}},
				},
				output: "partial output", runErr: fmt.Errorf("waiting for result: %w", context.Canceled), startedAt: time.Now(),
			})
			if run.reject != nil || run.failure == nil || run.failure.Code != toolrejection.ToolOwnerInterruptedCode ||
				run.failure.Class != "interrupted" || !run.failure.Retryable || !run.invoked ||
				run.facts.Resolution() != api.ToolResultOutcomeError || run.facts.PrimaryCode() != toolrejection.ToolOwnerInterruptedCode {
				t.Fatalf("canceled result lost interruption: failure=%+v facts=%+v", run.failure, run.facts)
			}
			if run.content != "partial output\nwaiting for result: context canceled" {
				t.Fatalf("result lost partial output: %q", run.content)
			}
		})
	}
}
