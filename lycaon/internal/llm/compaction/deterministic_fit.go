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

	keepFloor := len(body) - recentCompactionStart(body, max(1, cfg.KeepRecentMessages))
	// Over budget here, so a notice is certain; reserve its cost before sizing.
	budget := maxTokens - EstimateMessagesTokens(head) - EstimateMessagesTokens([]ContextMessage{contextTrimNotice()})
	drop := fitFrontDropCount(body, keepFloor, budget)

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

// splitCompactionCheckpointHead retains the checkpoint and its summary together.
func splitCompactionCheckpointHead(messages []ContextMessage) (head, body []ContextMessage) {
	if len(messages) >= 2 && messages[0].CompactionCheckpoint && messages[1].Role == string(api.MessageRoleAssistant) {
		return append([]ContextMessage(nil), messages[0], messages[1]), messages[2:]
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

// fitFrontDropCount rounds drops to block boundaries within the retention limit.
// Pinned rows remain in the budget on both sides of the cut.
func fitFrontDropCount(body []ContextMessage, keepFloor, budget int) int {
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
	for drop := 0; drop < maxDrop; drop++ {
		if pinnedPrefix[drop]+remaining[drop] > budget {
			continue
		}
		// Round up to the next block boundary, but never past the keep floor.
		blocked := (drop + fitDropBlockMessages - 1) / fitDropBlockMessages * fitDropBlockMessages
		if blocked > maxDrop {
			return maxDrop
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
		Origin:        api.MessageOriginHost,
		Authority:     api.ContentAuthoritySystem,
		TrustTier:     api.ContentTrustTierTrusted,
		ContextPinned: true,
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
