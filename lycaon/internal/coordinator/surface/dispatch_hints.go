package surface

import (
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/pkg/api"
)

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
