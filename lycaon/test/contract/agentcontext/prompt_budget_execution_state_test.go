//go:build budgets

package contract

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

func measureExecutionStateBlock(ctx context.Context, renderer *prompts.InjectRenderer) string {
	var history []api.Message
	for i := range 4 {
		for _, tool := range []string{"write", "command"} {
			id := fmt.Sprintf("01234567-0123-4567-8901-%012d", i)
			receipt := &api.InvocationReceipt{ID: id, Tool: tool, ToolCallID: id, ArgsDigest: id, Status: api.InvocationStatusRejected, Failure: &api.InvocationFailure{Code: "PROGRESS_MISSING"}}
			if tool == "command" {
				receipt.Status, receipt.Invoked, receipt.Failure = api.InvocationStatusCompleted, true, nil
				receipt.SourceVerdict, receipt.SourceRevision, receipt.SourceRootDigest = "failed", strings.Repeat("a", 64), strings.Repeat("b", 64)
			}
			history = append(history, api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
				Tool: tool, ToolCallID: id, Invocation: receipt,
				ToolArgs: map[string]any{"command": strings.Repeat("c", 254), "path": strings.Repeat("p", 254), "cwd": "."},
			}})
		}
	}
	return inject.RenderExecutionStateBlock(ctx, renderer, "sess-inject-test", history, "./task check")
}
