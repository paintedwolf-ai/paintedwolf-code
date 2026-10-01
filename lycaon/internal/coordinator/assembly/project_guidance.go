package assembly

import (
	"encoding/json"

	"github.com/lycaon/lycaon/pkg/api"
)

// deduplicateProjectGuidance coalesces identical policy observations at the same authority.
// Different scope, source revision or trust keeps a separate observation.
func deduplicateProjectGuidance(messages []api.Message) []api.Message {
	out := make([]api.Message, 0, len(messages))
	seen := make(map[string]bool)
	for _, msg := range messages {
		if msg.Origin == api.MessageOriginProject && msg.Authority == api.ContentAuthorityDeveloper && msg.TrustTier == api.ContentTrustTierTrusted {
			// Source context is retained on the first copy; unequal contexts must not collapse.
			key := projectGuidanceIdentity(msg)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, msg)
	}
	return out
}

func projectGuidanceIdentity(msg api.Message) string {
	raw, err := json.Marshal(struct {
		Content string
		Source  *api.SourceContext
		Parts   []api.MessageContentPart
	}{msg.Content, msg.SourceContext, msg.ContentParts})
	if err != nil {
		return msg.Content
	}
	return string(raw)
}
