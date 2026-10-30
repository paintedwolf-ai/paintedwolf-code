package policyindex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/pkg/api"
)

type deliveredPolicy struct {
	files   []agentpolicy.Revision
	content [sha256.Size]byte
}

func (m *Service) recordDelivery(message api.Message, revisions ...agentpolicy.Revision) api.Message {
	body, _ := json.Marshal(revisions)
	content := sha256.Sum256([]byte(message.Content))
	identity := sha256.Sum256(append(body, content[:]...))
	message.ID = "host-agent-policy-" + hex.EncodeToString(identity[:])
	m.delivered.Store(message.ID, deliveredPolicy{files: append([]agentpolicy.Revision(nil), revisions...), content: content})
	return message
}

// ManifestFor records only intact host policy blocks in the final model request.
func (m *Service) ManifestFor(messages []api.Message) agentpolicy.Manifest {
	manifest := agentpolicy.Manifest{}
	if m == nil {
		return manifest
	}
	seen := map[agentpolicy.Revision]bool{}
	for _, message := range messages {
		if message.Role != api.MessageRoleSystem || message.Origin != api.MessageOriginProject ||
			message.Authority != api.ContentAuthorityDeveloper || message.TrustTier != api.ContentTrustTierTrusted || len(message.ContentParts) != 0 {
			continue
		}
		record, ok := m.delivered.Load(message.ID)
		if !ok || sha256.Sum256([]byte(message.Content)) != record.content {
			continue
		}
		for _, revision := range record.files {
			if !seen[revision] {
				manifest.Files = append(manifest.Files, revision)
				seen[revision] = true
			}
		}
	}
	return manifest
}
