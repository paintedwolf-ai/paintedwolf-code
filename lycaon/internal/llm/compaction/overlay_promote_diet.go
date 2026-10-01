package compaction

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

const overlayPromoteDietTombstone = "[overlay-promote diet — elided; use preview_overlay or promote_overlay(overlay_id)]"

var overlayPromoteDietTools = map[string]struct{}{
	"preview_overlay": {},
	"promote_overlay": {},
	"reject_overlay":  {},
	"pack_board":      {},
}

// DietOverlayPromoteMessages trims investigation-era context on implement_overlay_promote
// turns: stale product reads, duplicate overlay assessments, and old pack_board snapshots.
func DietOverlayPromoteMessages(messages []api.Message) []api.Message {
	if len(messages) == 0 {
		return messages
	}
	calls := pairToolResultsToCalls(messages)
	keep := overlayPromoteKeepIndices(messages, calls)
	if len(keep) == 0 {
		return messages
	}
	out := make([]api.Message, len(messages))
	copy(out, messages)
	for i, m := range messages {
		if m.Role != api.MessageRoleTool {
			continue
		}
		call, ok := calls[i]
		if !ok {
			continue
		}
		name := strings.TrimSpace(strings.ToLower(call.Name))
		if _, dietTool := overlayPromoteDietTools[name]; dietTool {
			if !keep[i] {
				out[i].Content = overlayPromoteDietTombstone
			}
			continue
		}
		if name == "read" && overlayPromoteReadShouldElide(call.Args) {
			out[i].Content = fmt.Sprintf("[overlay-promote diet — product read of %s elided; use preview_overlay or promote_overlay(overlay_id)]", normalizeReadPath(call.Args["path"]))
		}
	}
	return out
}

func overlayPromoteKeepIndices(messages []api.Message, calls map[int]api.ToolCall) map[int]bool {
	keep := map[int]bool{}
	lastOverlay := map[string]int{}
	lastPackBoard := -1
	for i, m := range messages {
		if m.Role != api.MessageRoleTool {
			continue
		}
		call, ok := calls[i]
		if !ok {
			continue
		}
		name := strings.TrimSpace(strings.ToLower(call.Name))
		switch name {
		case "pack_board":
			lastPackBoard = i
		case "preview_overlay", "promote_overlay", "reject_overlay":
			overlayID := overlayPromoteToolOverlayID(call.Args)
			if overlayID == "" {
				continue
			}
			key := name + ":" + overlayID
			lastOverlay[key] = i
		}
	}
	if lastPackBoard >= 0 {
		keep[lastPackBoard] = true
	}
	for _, idx := range lastOverlay {
		keep[idx] = true
	}
	return keep
}

func overlayPromoteToolOverlayID(args map[string]any) string {
	if id := strings.TrimSpace(asString(args["overlay_id"])); id != "" {
		return id
	}
	return strings.TrimSpace(asString(args["job_id"]))
}

func overlayPromoteReadShouldElide(args map[string]any) bool {
	path := normalizeReadPath(args["path"])
	if path == "" {
		return false
	}
	// Keep agent spill reads (relative wire + absolute durable leftovers).
	if tooloutput.IsAgentWireSpillReadArg(path) {
		return false
	}
	return true
}
