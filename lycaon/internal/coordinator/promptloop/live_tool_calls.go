package promptloop

import (
	"strings"

	"github.com/lycaon/lycaon/internal/toolpresentation"
	"github.com/lycaon/lycaon/pkg/api"
)

// liveProjectedToolCalls keeps named long-running calls on the live row.
// These calls come from a partial completion, before the host admits them and
// while a reject can still stop them running, so only rows that survive any
// result may project. Everything else reaches the transcript through its result.
func liveProjectedToolCalls(calls []api.ToolCall) []api.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]api.ToolCall, 0, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" {
			continue
		}
		if !toolpresentation.IsLongRunning(call.Name) {
			continue
		}
		out = append(out, call)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
