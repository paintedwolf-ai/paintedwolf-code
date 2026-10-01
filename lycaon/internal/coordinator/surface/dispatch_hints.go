package surface

import (
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/pkg/api"
)

// SessionUserTask returns the first visible user message.
func SessionUserTask(history []api.Message) string {
	for _, msg := range history {
		if msg.Role != api.MessageRoleUser {
			continue
		}
		if isVisibleUserMessage(msg) {
			return msg.Content
		}
	}
	return ""
}

// SessionForwardedAttachments returns deduplicated openable user content.
func SessionForwardedAttachments(history []api.Message) []promptattach.ForwardedAttachment {
	var out []promptattach.ForwardedAttachment
	seen := map[string]struct{}{}
	for _, msg := range history {
		if !isVisibleUserMessage(msg) {
			continue
		}
		for _, h := range promptattach.ForwardedAttachmentsFromMessage(msg) {
			if h.Path == "" {
				continue
			}
			if _, ok := seen[h.Path]; ok {
				continue
			}
			seen[h.Path] = struct{}{}
			out = append(out, h)
		}
	}
	return out
}
