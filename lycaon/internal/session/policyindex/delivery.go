package policyindex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/pkg/api"
)

type deliveredPolicy struct {
	files   []agentpolicy.Revision
	content [sha256.Size]byte
}

type deliveredGroup map[string]deliveredPolicy

const policyMessagePrefix = "host-agent-policy-"

func (m *Service) recordDeliveries(messages []api.Message, revisions [][]agentpolicy.Revision) []api.Message {
	if len(messages) == 0 {
		return messages
	}
	records := make([]deliveredPolicy, len(messages))
	identities := make([]struct {
		Files   []agentpolicy.Revision
		Content [sha256.Size]byte
	}, len(messages))
	for i, message := range messages {
		records[i] = deliveredPolicy{files: append([]agentpolicy.Revision(nil), revisions[i]...), content: sha256.Sum256([]byte(message.Content))}
		identities[i].Files, identities[i].Content = records[i].files, records[i].content
	}
	body, _ := json.Marshal(identities)
	identity := sha256.Sum256(body)
	groupID := hex.EncodeToString(identity[:])
	group := deliveredGroup{}
	for i := range messages {
		messages[i].ID = policyMessagePrefix + groupID + "/" + strconv.Itoa(i)
		group[messages[i].ID] = records[i]
	}
	m.delivered.Store(groupID, group)
	return messages
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
		identity, ok := strings.CutPrefix(message.ID, policyMessagePrefix)
		groupID, _, qualified := strings.Cut(identity, "/")
		if !ok || !qualified {
			continue
		}
		group, ok := m.delivered.Load(groupID)
		if !ok {
			continue
		}
		record, ok := group[message.ID]
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
