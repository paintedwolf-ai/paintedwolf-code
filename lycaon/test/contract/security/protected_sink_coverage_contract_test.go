package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/settings"
)

// nonPathMutatingAck names each gateable tool that writes no path from its arguments,
// with the reason. The substrate sink denial gates on a closed list, so a mutating tool
// missing from it would reach approvals.yaml or mcp.yaml with no floor denial; a tool
// that takes a path and writes to it belongs in settings.pathMutatingToolIDs.
var nonPathMutatingAck = map[string]string{
	"command":           "argv, not a path argument — the write jail and the path-escape tiers govern where it can write",
	"command_stop":      "stops a background command job by id; no filesystem destination",
	"process_signal":    "signals processes by reference; no filesystem path argument",
	"terminal_open":     "same as command",
	"terminal_send":     "writes bytes to a pty, not to a path",
	"terminal_read":     "reads terminal output",
	"terminal_snapshot": "reads terminal state",
	"terminal_close":    "closes a terminal session",

	"chmod": "changes mode, not content — a sink's bytes are unaffected",
	"chown": "changes ownership, not content",

	"fetch_url":     "network read; no filesystem destination",
	"http_request":  "network request; body_path is read-only and no caller-named path is written",
	"web_search":    "network read; no filesystem destination",
	"secret_revoke": "removes a protected-store value by managed reference; no filesystem path argument",

	"capture_page":  "writes a host artifact under the config dir; takes no caller-named path",
	"render_view":   "same as capture_page",
	"measure_page":  "returns measurements; writes nothing",
	"page_open":     "drives the managed browser; no filesystem destination",
	"page_act":      "same as page_open",
	"page_close":    "same as page_open",
	"page_snapshot": "returns a snapshot to the caller; no filesystem destination",

	"task": "spawns a worker session; any path the worker touches is gated on the worker's own tool call",
}

// Every tool the approval layer can gate either writes to a caller-named path — and so
// must be covered by the substrate sink denial — or is explicitly acknowledged above.
func TestEveryMutatingToolIsSinkGuarded(t *testing.T) {
	t.Parallel()
	gateable := append(
		append([]string(nil), settings.ApprovalRecoverableToolIDs()...),
		settings.ApprovalIrreversibleToolIDs()...,
	)
	if len(gateable) == 0 {
		t.Fatal("no gateable tools resolved; this guard would pass vacuously")
	}

	for _, tool := range gateable {
		if settings.IsPathMutatingTool(tool) {
			continue
		}
		if _, acked := nonPathMutatingAck[tool]; acked {
			continue
		}
		t.Errorf("tool %q is gateable but neither sink-guarded nor acknowledged: if it writes to a path from its arguments add it to settings.pathMutatingToolIDs, otherwise add it to nonPathMutatingAck with the reason", tool)
	}
}

// TestSinkAckHasNoStaleEntries requires every acknowledgement to name a current tool.
func TestSinkAckHasNoStaleEntries(t *testing.T) {
	t.Parallel()
	known := make(map[string]bool)
	for _, id := range settings.ApprovalRecoverableToolIDs() {
		known[id] = true
	}
	for _, id := range settings.ApprovalIrreversibleToolIDs() {
		known[id] = true
	}
	for tool := range nonPathMutatingAck {
		if !known[tool] {
			t.Errorf("acknowledged tool %q is not a gateable tool any more; drop the ack", tool)
		}
	}
}
