package compaction

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// fitDropBlockMessages groups front drops to stabilize the retained prefix.
const fitDropBlockMessages = 32

// contextTrimNoticeID identifies the transient host trim notice.
const contextTrimNoticeID = "host-context-trim-notice"

// DeterministicFit drops old rows in blocks while preserving checkpoints, pins, and recent exchanges.
// Protected rows may leave the result over budget.
func DeterministicFit(cfg CompactionConfig, messages []ContextMessage, maxTokens int) []ContextMessage {
	return DeterministicFitWithReserve(cfg, messages, maxTokens, 0)
}

// DeterministicFitWithReserve also aims to leave reserve tokens free after a
// required drop, so the cut it chooses can hold across later appends.
func DeterministicFitWithReserve(cfg CompactionConfig, messages []ContextMessage, maxTokens, reserve int) []ContextMessage {
	// Repeated fitting retains one notice even when a later pass fits.
	trimmed := hasContextTrimNotice(messages)
	messages = withoutContextTrimNotice(messages)
	head, body := splitCompactionCheckpointHead(messages)
	noticeCost := 0
	if trimmed {
		noticeCost = EstimateMessagesTokens([]ContextMessage{contextTrimNotice()})
	}
	if maxTokens <= 0 || len(messages) == 0 || EstimateMessagesTokens(messages)+noticeCost <= maxTokens {
		return assembleFit(head, body, trimmed)
	}

	// Over budget here, so a notice is certain; reserve its cost before sizing.
	budget := maxTokens - EstimateMessagesTokens(head) - EstimateMessagesTokens([]ContextMessage{contextTrimNotice()})
	defaultFloor := len(body) - recentCompactionStart(body, max(1, cfg.KeepRecentMessages))
	recent := recentCompactionCount(cfg, body, budget)
	keepFloor := len(body) - recentCompactionStart(body, recent)
	drop := fitFrontDropCount(body, keepFloor, defaultFloor, budget, reserve)
	drop = completeFitStart(body, drop)

	pinned := pinnedRows(body[:drop])
	kept := alignCompactionTail(body[drop:])
	return assembleFit(head, joinRows(pinned, kept), trimmed || drop > 0)
}

// joinRows concatenates into fresh backing without changing the source slices.
func joinRows(pinned, kept []ContextMessage) []ContextMessage {
	out := make([]ContextMessage, 0, len(pinned)+len(kept))
	out = append(out, pinned...)
	return append(out, kept...)
}

// assembleFit places the trim notice after checkpoints and leading pinned rows.
func assembleFit(head, body []ContextMessage, trimmed bool) []ContextMessage {
	out := make([]ContextMessage, 0, len(head)+len(body)+1)
	out = append(out, head...)
	if !trimmed {
		return append(out, body...)
	}
	lead := 0
	for lead < len(body) && body[lead].ContextPinned {
		lead++
	}
	out = append(out, body[:lead]...)
	out = append(out, contextTrimNotice())
	return append(out, body[lead:]...)
}

// splitCompactionCheckpointHead retains the checkpoint and its summary together,
// with any pinned rows placed before them, such as an opener preceding intra-turn coverage.
func splitCompactionCheckpointHead(messages []ContextMessage) (head, body []ContextMessage) {
	for i := range messages {
		if messages[i].CompactionCheckpoint {
			if i+1 < len(messages) && messages[i+1].Role == string(api.MessageRoleAssistant) {
				return append([]ContextMessage(nil), messages[:i+2]...), messages[i+2:]
			}
			return nil, messages
		}
		if !messages[i].ContextPinned {
			break
		}
	}
	return nil, messages
}

func pinnedRows(messages []ContextMessage) []ContextMessage {
	var out []ContextMessage
	for i := range messages {
		if messages[i].ContextPinned {
			out = append(out, messages[i])
		}
	}
	return out
}

// completeFitStart advances past results whose call the cut would strand,
// including parallel results interleaved with host rows.
func completeFitStart(body []ContextMessage, drop int) int {
	start := drop
	for {
		calls := make(map[string]bool)
		orphan := -1
		for i := start; i < len(body); i++ {
			for _, call := range body[i].ToolCalls {
				calls[call.ID] = true
			}
			if body[i].Role == string(api.MessageRoleTool) && body[i].ToolCallID != "" && !calls[body[i].ToolCallID] {
				orphan = i
			}
		}
		if orphan < 0 {
			return start
		}
		if orphan+1 >= len(body) {
			return drop
		}
		start = orphan + 1
	}
}

// fitAnchorDrop places a required drop on the stable ordinal grid: the nearest
// anchor to its reserve target that still moves past the minimum. Rows without
// ordinals round by position.
func fitAnchorDrop(body []ContextMessage, minimum, target int) int {
	if target >= len(body) || body[target].Ord <= 0 {
		return (target + fitDropBlockMessages - 1) / fitDropBlockMessages * fitDropBlockMessages
	}
	anchor := (body[target].Ord + fitDropBlockMessages/2) / fitDropBlockMessages * fitDropBlockMessages
	for {
		at := len(body)
		for i := range body {
			if body[i].Ord >= anchor && body[i].Ord > 0 {
				at = i
				break
			}
		}
		if at > minimum || at == len(body) {
			return at
		}
		anchor += fitDropBlockMessages
	}
}

// fitFrontDropCount rounds drops to block boundaries within the retention limit.
// Pinned rows remain in the budget on both sides of the cut.
func fitFrontDropCount(body []ContextMessage, keepFloor, defaultFloor, budget, reserve int) int {
	if len(body) <= keepFloor {
		return 0
	}
	// remaining[i] is the cost of body[i:]; pinnedPrefix[i] the pinned rows in
	// body[:i], which survive either way.
	remaining := make([]int, len(body)+1)
	pinnedPrefix := make([]int, len(body)+1)
	for i := len(body) - 1; i >= 0; i-- {
		remaining[i] = remaining[i+1] + EstimateMessagesTokens(body[i:i+1])
	}
	for i := range body {
		pinnedPrefix[i+1] = pinnedPrefix[i]
		if body[i].ContextPinned {
			pinnedPrefix[i+1] += EstimateMessagesTokens(body[i : i+1])
		}
	}
	maxDrop := len(body) - keepFloor
	blockCap := min(maxDrop, max(0, len(body)-defaultFloor))
	for drop := 0; drop < maxDrop; drop++ {
		if pinnedPrefix[drop]+remaining[drop] > budget {
			continue
		}
		if drop >= blockCap {
			return drop
		}
		if drop == 0 {
			return 0
		}
		target := drop
		for target < blockCap && pinnedPrefix[target]+remaining[target] > budget-reserve {
			target++
		}
		// Stay on the grid, but never past the recent window floor.
		blocked := fitAnchorDrop(body, drop, target)
		if blocked > blockCap {
			return blockCap
		}
		return blocked
	}
	return maxDrop
}

// contextTrimNotice omits counts because later fitting passes may remove more rows.
func contextTrimNotice() ContextMessage {
	return ContextMessage{
		ID:            contextTrimNoticeID,
		Role:          string(api.MessageRoleSystem),
		Content:       guidance.ContextTrimNotice,
		ContextPinned: true,
		ContextMessageProvenance: ContextMessageProvenance{
			Origin:    api.MessageOriginHost,
			Authority: api.ContentAuthoritySystem,
			TrustTier: api.ContentTrustTierTrusted,
		},
	}
}

// hasContextTrimNotice reports whether an earlier pass already stated the cut.
func hasContextTrimNotice(messages []ContextMessage) bool {
	for i := range messages {
		if messages[i].ID == contextTrimNoticeID {
			return true
		}
	}
	return false
}

// withoutContextTrimNotice removes the transient notice before reassembly.
func withoutContextTrimNotice(messages []ContextMessage) []ContextMessage {
	kept := messages[:0:0]
	for i := range messages {
		if messages[i].ID == contextTrimNoticeID {
			continue
		}
		kept = append(kept, messages[i])
	}
	return kept
}
