package api

import "strings"

const (
	draftRecoverySignalPrefix = "draft-recovery:"
	repairSignalPrefix        = "guidance:"
)

// DraftRecoverySignal binds host guidance to the draft whose replacement consumes it.
// The suffix retains the guidance occurrence identity for deduplication.
func DraftRecoverySignal(draftID, signalID string) string {
	if draftID == "" || strings.Contains(draftID, ":") {
		return signalID
	}
	return draftRecoverySignalPrefix + draftID + ":" + signalID
}

// RepairSignal identifies guidance that repairs a refused attempt. A subject
// scopes the repair to one target.
func RepairSignal(feedback ToolFeedback) string {
	code := strings.TrimSpace(feedback.Code)
	if code == "" {
		return ""
	}
	if feedback.Subject == nil {
		return repairSignalPrefix + code
	}
	kind := strings.TrimSpace(feedback.Subject.Kind)
	id := strings.TrimSpace(feedback.Subject.ID)
	if kind == "" || id == "" {
		return repairSignalPrefix + code
	}
	return repairSignalPrefix + code + ":" + kind + ":" + id
}

// IsActiveRepair reports repair guidance that still addresses the model.
func IsActiveRepair(message Message) bool {
	if !isHostGuidance(message) || message.DraftStatus == DraftStatusWithdrawn {
		return false
	}
	signal := message.HostSignalID
	if rest, ok := strings.CutPrefix(signal, draftRecoverySignalPrefix); ok {
		_, signal, _ = strings.Cut(rest, ":")
	}
	return strings.HasPrefix(signal, repairSignalPrefix)
}

func isHostGuidance(message Message) bool {
	return message.Role == MessageRoleUser && message.Origin == MessageOriginHost &&
		message.Authority == ContentAuthoritySystem && message.Visibility == MessageVisibilityInternal &&
		message.Kind == MessageKindCoordinatorGuidance
}

// DraftRecoveryTarget returns the draft message ID targeted by a draft-recovery
// host guidance row, or empty when message is not a draft-recovery signal.
func DraftRecoveryTarget(message Message) string {
	if !isHostGuidance(message) {
		return ""
	}
	rest, ok := strings.CutPrefix(message.HostSignalID, draftRecoverySignalPrefix)
	if !ok {
		return ""
	}
	id, _, ok := strings.Cut(rest, ":")
	if !ok {
		return ""
	}
	return id
}

// orderDraftRecovery restores causal order without moving immutable transcript rows.
// A stable draft rail can precede guidance that its replacement already consumed.
func orderDraftRecovery(messages []Message) []Message {
	drafts := make(map[string]int)
	for i, message := range messages {
		if message.ID != "" && message.Role == MessageRoleAssistant {
			drafts[message.ID] = i
		}
	}
	before := make(map[int][]Message)
	moved := make(map[int]bool)
	for i, message := range messages {
		if target, ok := drafts[DraftRecoveryTarget(message)]; ok && target < i {
			before[target] = append(before[target], message)
			moved[i] = true
		}
	}
	if len(moved) == 0 {
		return messages
	}
	ordered := make([]Message, 0, len(messages))
	for i, message := range messages {
		ordered = append(ordered, before[i]...)
		if !moved[i] {
			ordered = append(ordered, message)
		}
	}
	return ordered
}
