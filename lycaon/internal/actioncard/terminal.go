package actioncard

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func init() {
	register(terminalResult, "terminal_open", "terminal_send", "terminal_read", "terminal_snapshot", "terminal_close")
}

// terminalStep names each terminal tool in a session's step sequence.
var terminalStep = map[string]string{
	"terminal_open": "open", "terminal_send": "send", "terminal_read": "read",
	"terminal_snapshot": "snapshot", "terminal_close": "close",
}

// terminalResult describes one call of a terminal session. Calls on one
// terminal share a fold key, and Den shows the session as one card.
func terminalResult(result api.ToolResult) Parts {
	parts := Parts{Noun: "command", FoldKey: terminalFoldKey(result)}
	if command, ok := result.ToolArgs["command"].(string); ok && strings.TrimSpace(command) != "" {
		parts.Ran = command
	}
	step := terminalStep[result.Tool]
	parts.Facts = append(parts.Facts, api.ActionFact{Kind: api.ActionFactSteps, Count: 1, Unit: "actions", Sequence: []string{step}})
	switch {
	case resultStatus(result) == statusError:
		parts.Facts = append(parts.Facts, resultFact(rejectedOutcome(result)))
		if code := firstCode(result); code != "" {
			parts.Facts = append(parts.Facts, stoppedByFact("host_stop", code))
		}
		parts.Row = []api.ActionFactKind{api.ActionFactResult}
	case result.Tool == "terminal_close":
		parts.Facts = append(parts.Facts, resultFact("completed"))
		parts.Row = []api.ActionFactKind{api.ActionFactResult, api.ActionFactSteps}
	default:
		parts.Facts = append(parts.Facts, processFact("running"))
		parts.Row = []api.ActionFactKind{api.ActionFactSteps}
	}
	if handle := terminalHandle(result); handle != "" {
		parts.Opener = &api.ActionOpener{Kind: "terminal", ProcessHandle: handle}
	}
	return parts
}

// terminalHandle is the session: the handle terminal_open returned, or the
// id a later call names.
func terminalHandle(result api.ToolResult) string {
	if result.Process != nil && result.Process.Handle != "" {
		return result.Process.Handle
	}
	id, _ := result.ToolArgs["id"].(string)
	return strings.TrimSpace(id)
}

func terminalFoldKey(result api.ToolResult) string {
	if handle := terminalHandle(result); handle != "" {
		return "terminal:" + handle
	}
	return ""
}
