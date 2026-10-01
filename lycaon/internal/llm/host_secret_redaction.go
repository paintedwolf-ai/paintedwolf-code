package llm

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// secretReplacements is what the host wrote into one request copy, by kind.
type secretReplacements struct {
	redactions, references int
}

func (r *secretReplacements) add(kind api.RedactionKind) {
	if kind == api.RedactionKindManagedReference {
		r.references++
		return
	}
	r.redactions++
}

func (r *secretReplacements) addMeta(meta *api.HostSecretRedactionMeta) {
	r.redactions += meta.Redactions()
	r.references += meta.References()
}

func (r secretReplacements) empty() bool { return r.redactions == 0 && r.references == 0 }

// HostSecretRedactionNotice describes replacements selected by span metadata.
func HostSecretRedactionNotice(redactions, references bool) string {
	if !redactions && !references {
		return ""
	}
	var b strings.Builder
	b.WriteString("Host security: This host ")
	switch {
	case redactions && references:
		b.WriteString("redacted secret values in this request and replaced protected ones with `{{paintedwolf-secret:…}}` references. ")
	case references:
		b.WriteString("replaced protected secret values in this request with `{{paintedwolf-secret:…}}` references. ")
	default:
		b.WriteString("redacted secret values in this request. ")
	}
	b.WriteString("Only model and transcript copies changed; the underlying sources still hold the real values. ")
	b.WriteString("Reading the same source again returns the same copy, so re-read it only if you need its other content.")
	if references {
		b.WriteString(" A reference is not a placeholder or a broken value. Pass it unchanged to a tool that resolves secret references, " +
			"including file-writing tools when configuring credentials (the host writes the value when the file is saved; your approval posture decides whether you're asked first).")
	}
	return b.String()
}

// AppendHostSecretRedactionNotice adds one notice for the replacements in messages.
func AppendHostSecretRedactionNotice(messages []api.Message) []api.Message {
	return appendHostSecretRedactionNotice(messages, secretReplacements{})
}

// appendHostSecretRedactionNotice also counts tool-definition replacements. An
// existing notice is rewritten in place, since a later screen may add a kind.
func appendHostSecretRedactionNotice(messages []api.Message, definitions secretReplacements) []api.Message {
	written := definitions
	existing := -1
	for i, msg := range messages {
		if msg.Kind == api.MessageKindHostSecretRedactionNotice {
			existing = i
			continue
		}
		written.addMeta(msg.HostSecretRedaction)
	}
	if written.empty() {
		return messages
	}
	content := HostSecretRedactionNotice(written.redactions > 0, written.references > 0)
	if existing >= 0 {
		if messages[existing].Content == content {
			return messages
		}
		out := append([]api.Message(nil), messages...)
		out[existing].Content = content
		return out
	}
	return append(append([]api.Message(nil), messages...), api.Message{
		Role:      api.MessageRoleSystem,
		Kind:      api.MessageKindHostSecretRedactionNotice,
		Content:   content,
		Origin:    api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem,
		TrustTier: api.ContentTrustTierTrusted,
	})
}
