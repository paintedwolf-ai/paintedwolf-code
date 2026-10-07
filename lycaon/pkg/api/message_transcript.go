package api

import (
	"strings"
)

// IsWorkflowBoundaryMessage reports span bookkeeping rows — never chat, never LLM history.
func IsWorkflowBoundaryMessage(msg Message) bool {
	return msg.Kind == MessageKindWorkflowBoundary || msg.WorkflowBoundary != nil
}

// IsInternalTranscriptMessage reports host rows hidden from Den chat (kicks, ambient boundaries).
func IsInternalTranscriptMessage(msg Message) bool {
	return msg.Visibility == MessageVisibilityInternal
}

// IsProgressCompleteMessage reports a completed-run progress snapshot — shown in Den chat as a
// read-only progress bar, never sent to the model as history.
func IsProgressCompleteMessage(msg Message) bool {
	return msg.Kind == MessageKindProgressComplete || msg.ProgressComplete != nil
}

// IsProgressUpdateMessage reports a mid-run progress delta — shown in Den chat as a strip of plan
// changes, never sent to the model as history.
func IsProgressUpdateMessage(msg Message) bool {
	return msg.Kind == MessageKindProgressUpdate || msg.ProgressUpdate != nil
}

// IsWorkflowFeedbackMessage reports a phase's open-ended question — shown in Den chat as a
// persistent question card, never sent to the model as history (it is host-authored UI copy).
func IsWorkflowFeedbackMessage(msg Message) bool {
	return msg.Kind == MessageKindWorkflowFeedback || msg.WorkflowFeedback != nil
}

// IsWorkflowExplainMessage reports the host's note for a phase it holds. The
// note is written for the person; the coordinator reads the same facts itself.
func IsWorkflowExplainMessage(msg Message) bool {
	return msg.Kind == MessageKindWorkflowExplain || msg.WorkflowExplain != nil
}

// IsBlueprintMessage reports a blueprint proposal card row — shown in Den chat inline by ord, never
// sent to the model as history (the blueprint body is host-authored UI copy).
func IsBlueprintMessage(msg Message) bool {
	return msg.Kind == MessageKindBlueprint || msg.Blueprint != nil
}

// IsUserIntentMessage reports a message the user themselves sent — the rows that
// open a turn. Host kicks, ambient boundaries, and workflow span bookkeeping are
// user-role rows that carry no user intent.
func IsUserIntentMessage(msg Message) bool {
	return msg.Role == MessageRoleUser &&
		msg.Kind != MessageKindUserContinuation &&
		!IsInternalTranscriptMessage(msg) &&
		!IsWorkflowBoundaryMessage(msg)
}

// IsUserInstructionMessage reports visible user-authored direction.
func IsUserInstructionMessage(msg Message) bool {
	return msg.Role == MessageRoleUser &&
		msg.Origin == MessageOriginUser &&
		!IsInternalTranscriptMessage(msg) &&
		!IsWorkflowBoundaryMessage(msg)
}

// UserIntentBoundary returns the history index after the most recent visible user
// intent message. Internal host rows and workflow span boundaries are skipped.
// Returns 0 when no visible user intent exists.
func UserIntentBoundary(history []Message) int {
	for i := len(history) - 1; i >= 0; i-- {
		if IsUserIntentMessage(history[i]) {
			return i + 1
		}
	}
	return 0
}

// LastUserIntentMessage returns the most recent visible user intent message.
func LastUserIntentMessage(history []Message) (Message, bool) {
	if i := UserIntentBoundary(history); i > 0 {
		return history[i-1], true
	}
	return Message{}, false
}

// UserTurnOrdinal counts visible user intents; zero means no turn has started.
func UserTurnOrdinal(history []Message) int {
	n := 0
	for _, msg := range history {
		if IsUserIntentMessage(msg) {
			n++
		}
	}
	return n
}

// IsTranscriptChromeMessage reports UI transcript rows that do not enter
// model-facing history. Classification is structural rather than content shape.
func IsTranscriptChromeMessage(msg Message) bool {
	return IsWorkflowBoundaryMessage(msg) ||
		IsProgressCompleteMessage(msg) ||
		IsProgressUpdateMessage(msg) ||
		IsWorkflowFeedbackMessage(msg) ||
		IsWorkflowExplainMessage(msg) ||
		IsIndexWarmingMessage(msg) ||
		IsBlueprintMessage(msg)
}

// IsRetractedAttemptMessage reports an assistant attempt retracted before becoming
// canonical, including withdrawn proposals, superseded reports, and rejected coordinator attempts.
func IsRetractedAttemptMessage(msg Message) bool {
	return msg.DraftStatus == DraftStatusWithdrawn ||
		msg.DraftStatus == DraftStatusRejected ||
		msg.Kind == MessageKindSuperseded
}

// IsPromptHistoryMessage reports whether a stored row belongs in LLM completion history.
// Internal host user nudges remain visible to the model; transcript chrome and retracted
// attempts do not.
func IsPromptHistoryMessage(msg Message) bool {
	return !IsTranscriptChromeMessage(msg) && !IsRetractedAttemptMessage(msg)
}

// IsIndexWarmingMessage identifies transcript-only index warming.
func IsIndexWarmingMessage(msg Message) bool {
	return msg.Kind == MessageKindIndexWarming || msg.IndexWarming != nil
}

// SpanScrollAnchor returns the message id history should scroll to for a workflow span start.
// Prefers the first visible user row (e.g. slash start); catalog runs fall back to the
// transcript-visible start boundary when no user row exists yet.
func SpanScrollAnchor(appended []Message, boundary Message) string {
	for _, msg := range appended {
		if IsWorkflowBoundaryMessage(msg) {
			continue
		}
		if msg.Role == MessageRoleUser && strings.TrimSpace(msg.Content) != "" {
			return msg.ID
		}
	}
	if !IsInternalTranscriptMessage(boundary) && strings.TrimSpace(boundary.ID) != "" {
		return boundary.ID
	}
	return ""
}

// IsInternalHostUserNudge reports a host steering row the model reads but Den hides.
func IsInternalHostUserNudge(msg Message) bool {
	msg = NormalizeMessageProvenance(msg)
	return msg.Role == MessageRoleUser && msg.Visibility == MessageVisibilityInternal &&
		msg.Origin == MessageOriginHost && msg.Authority == ContentAuthoritySystem
}

// IsAgentNoteMessage reports a user-visible mid-turn assistant note.
func IsAgentNoteMessage(msg Message) bool {
	return msg.Role == MessageRoleAssistant && msg.Kind == MessageKindAgentNote
}

// EnsureToolCallGroupContiguityForPrompt moves intervening host and agent notes
// after tool results in model-facing history. Persisted order stays unchanged.
func EnsureToolCallGroupContiguityForPrompt(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}
	type pendingGroup struct {
		remaining map[string]struct{}
	}
	var (
		out      []Message
		group    *pendingGroup
		deferred []Message
	)
	flushDeferred := func() {
		if len(deferred) == 0 {
			return
		}
		out = append(out, deferred...)
		deferred = nil
	}
	closeGroup := func() {
		if group == nil {
			return
		}
		flushDeferred()
		group = nil
	}
	toolCallIDFromResult := func(msg Message) string {
		if msg.ToolResult != nil {
			return strings.TrimSpace(msg.ToolResult.ToolCallID)
		}
		return ""
	}
	for _, msg := range messages {
		if msg.Role == MessageRoleAssistant && len(msg.ToolCalls) > 0 {
			closeGroup()
			out = append(out, msg)
			remaining := make(map[string]struct{})
			for _, tc := range msg.ToolCalls {
				if id := strings.TrimSpace(tc.ID); id != "" {
					remaining[id] = struct{}{}
				}
			}
			group = &pendingGroup{remaining: remaining}
			if len(group.remaining) == 0 {
				closeGroup()
			}
			continue
		}
		if group != nil {
			if msg.Role == MessageRoleTool {
				tcid := toolCallIDFromResult(msg)
				if tcid != "" {
					if _, ok := group.remaining[tcid]; ok {
						delete(group.remaining, tcid)
						out = append(out, msg)
						if len(group.remaining) == 0 {
							closeGroup()
						}
						continue
					}
				}
				out = append(out, msg)
				continue
			}
			if IsInternalHostUserNudge(msg) || IsAgentNoteMessage(msg) {
				deferred = append(deferred, msg)
				continue
			}
			closeGroup()
			out = append(out, msg)
			continue
		}
		flushDeferred()
		out = append(out, msg)
	}
	flushDeferred()
	return out
}

// CloseToolCallGroupsForPrompt drops unanswered calls and empty assistant rows.
// Tool results remain visible even when their call is absent.
func CloseToolCallGroupsForPrompt(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}
	answered := make(map[string]struct{})
	for _, msg := range messages {
		if msg.Role != MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if id := strings.TrimSpace(msg.ToolResult.ToolCallID); id != "" {
			answered[id] = struct{}{}
		}
	}
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role != MessageRoleAssistant || len(msg.ToolCalls) == 0 {
			out = append(out, msg)
			continue
		}
		kept := make([]ToolCall, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			if _, ok := answered[strings.TrimSpace(tc.ID)]; ok {
				kept = append(kept, tc)
			}
		}
		if len(kept) == len(msg.ToolCalls) {
			out = append(out, msg)
			continue
		}
		if len(kept) == 0 && strings.TrimSpace(msg.Content) == "" {
			continue
		}
		msg.ToolCalls = kept
		out = append(out, msg)
	}
	return out
}

// FilterPromptHistory is the model-history projection: transcript chrome and retracted
// attempts are removed, unanswered tool calls are closed, and surviving groups are made
// contiguous — all before diet, fit, or provider assembly. Lossy intermediate IRs can
// drop Kind, so every boundary that builds model-facing messages applies it.
func FilterPromptHistory(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if IsPromptHistoryMessage(msg) {
			out = append(out, msg)
		}
	}
	return EnsureToolCallGroupContiguityForPrompt(CloseToolCallGroupsForPrompt(out))
}

// RehydrateTranscriptProjectionFields restores transcript projection fields that
// compaction/diet IRs do not model. Identity, ordering, visibility, and typed
// lifecycle classifiers remain canonical across a lossy content round trip.
func RehydrateTranscriptProjectionFields(projected, canonical Message) Message {
	if projected.ID == "" || canonical.ID == "" || projected.ID != canonical.ID {
		return projected
	}
	projected.Kind = canonical.Kind
	projected.WorkflowRunID = canonical.WorkflowRunID
	projected.Visibility = canonical.Visibility
	projected.Ord = canonical.Ord
	projected.Seq = canonical.Seq
	projected.DraftVersionCount = canonical.DraftVersionCount
	projected.DraftStatus = canonical.DraftStatus
	if projected.HostSecretRedaction == nil {
		projected.HostSecretRedaction = canonical.HostSecretRedaction
	}
	if projected.WorkflowBoundary == nil {
		projected.WorkflowBoundary = canonical.WorkflowBoundary
	}
	if projected.ProgressComplete == nil {
		projected.ProgressComplete = canonical.ProgressComplete
	}
	if projected.ProgressUpdate == nil {
		projected.ProgressUpdate = canonical.ProgressUpdate
	}
	if projected.WorkflowFeedback == nil {
		projected.WorkflowFeedback = canonical.WorkflowFeedback
	}
	if projected.WorkflowExplain == nil {
		projected.WorkflowExplain = canonical.WorkflowExplain
	}
	if projected.IndexWarming == nil {
		projected.IndexWarming = canonical.IndexWarming
	}
	if projected.Blueprint == nil {
		projected.Blueprint = canonical.Blueprint
	}
	if projected.WorkerSummary == nil {
		projected.WorkerSummary = canonical.WorkerSummary
	}
	if projected.Grounding == nil {
		projected.Grounding = canonical.Grounding
	}
	if len(projected.NavigationRefs) == 0 {
		projected.NavigationRefs = canonical.NavigationRefs
	}
	if len(projected.ArtifactIDs) == 0 {
		projected.ArtifactIDs = canonical.ArtifactIDs
	}
	return projected
}
