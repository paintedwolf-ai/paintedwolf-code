package compaction

// retainedCompactionMessages keeps pinned instructions and complete tool exchanges
// at the recent-history boundary, in their original order.
func retainedCompactionMessages(messages []ContextMessage, keepRecent int) []ContextMessage {
	start := recentCompactionStart(messages, keepRecent)
	var out []ContextMessage
	for i, msg := range messages {
		if i >= start || msg.ContextPinned {
			out = append(out, msg)
		}
	}
	return out
}

// System injects do not consume conversation retention. A parallel tool batch
// stays with its producing assistant row even when it crosses the count boundary.
func recentCompactionStart(messages []ContextMessage, keepRecent int) int {
	if keepRecent <= 0 {
		return 0
	}
	start := len(messages)
	for start > 0 && keepRecent > 0 {
		start--
		if messages[start].Role != "system" {
			keepRecent--
		}
	}
	for start > 0 && start < len(messages) && messages[start].Role == "tool" {
		start--
	}
	return start
}

func retainedMessageIDs(messages []ContextMessage) map[string]struct{} {
	out := make(map[string]struct{}, len(messages))
	for _, msg := range messages {
		if msg.ID != "" {
			out[msg.ID] = struct{}{}
		}
	}
	return out
}

func removedCompactionMessages(all, retained []ContextMessage) []ContextMessage {
	kept := retainedMessageIDs(retained)
	var out []ContextMessage
	for _, msg := range all {
		if _, ok := kept[msg.ID]; !ok {
			out = append(out, msg)
		}
	}
	return out
}
