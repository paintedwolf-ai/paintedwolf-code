package api

import "strings"

const draftRecoverySignalPrefix = "draft-recovery:"

// DraftRecoverySignal binds host guidance to the draft whose replacement consumes it.
// The suffix retains the guidance occurrence identity for deduplication.
func DraftRecoverySignal(draftID, signalID string) string {
	if draftID == "" || strings.Contains(draftID, ":") {
		return signalID
	}
	return draftRecoverySignalPrefix + draftID + ":" + signalID
}

func draftRecoveryTarget(message Message) string {
	if message.Role != MessageRoleUser || message.Origin != MessageOriginHost ||
		message.Authority != ContentAuthoritySystem || message.Visibility != MessageVisibilityInternal ||
		message.Kind != MessageKindCoordinatorGuidance {
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
		if target, ok := drafts[draftRecoveryTarget(message)]; ok && target < i {
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
