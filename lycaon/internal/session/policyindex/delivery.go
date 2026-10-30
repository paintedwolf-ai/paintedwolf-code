package policyindex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/pkg/api"
)

type deliveredPolicy struct {
	files    []agentpolicy.Revision
	content  [sha256.Size]byte
	priority int
}

type deliveredGroup map[string]deliveredPolicy

const policyMessagePrefix = "host-agent-policy-"

func (m *Service) recordDeliveries(messages []api.Message, revisions [][]agentpolicy.Revision, priorities []int) []api.Message {
	if len(messages) == 0 {
		return messages
	}
	records := make([]deliveredPolicy, len(messages))
	identities := make([]struct {
		Files    []agentpolicy.Revision
		Content  [sha256.Size]byte
		Priority int
	}, len(messages))
	for i, message := range messages {
		records[i] = deliveredPolicy{files: append([]agentpolicy.Revision(nil), revisions[i]...), content: sha256.Sum256([]byte(message.Content))}
		if len(priorities) > i {
			records[i].priority = priorities[i]
		}
		identities[i].Files, identities[i].Content, identities[i].Priority = records[i].files, records[i].content, records[i].priority
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
		record, ok := m.deliveredMessage(message)
		if !ok {
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

func (m *Service) deliveredMessage(message api.Message) (deliveredPolicy, bool) {
	if m == nil || message.Role != api.MessageRoleSystem || message.Origin != api.MessageOriginProject ||
		message.Authority != api.ContentAuthorityDeveloper || message.TrustTier != api.ContentTrustTierTrusted || len(message.ContentParts) != 0 {
		return deliveredPolicy{}, false
	}
	identity, ok := strings.CutPrefix(message.ID, policyMessagePrefix)
	groupID, _, qualified := strings.Cut(identity, "/")
	if !ok || !qualified {
		return deliveredPolicy{}, false
	}
	group, ok := m.delivered.Load(groupID)
	if !ok {
		return deliveredPolicy{}, false
	}
	record, ok := group[message.ID]
	return record, ok && sha256.Sum256([]byte(message.Content)) == record.content
}

// PolicyMessageGroups returns authenticated chains in working-scope priority order.
func (m *Service) PolicyMessageGroups(messages []api.Message) [][]api.Message {
	byPriority := map[int][]int{}
	records := map[int]deliveredPolicy{}
	for index, message := range messages {
		if record, ok := m.deliveredMessage(message); ok {
			byPriority[record.priority] = append(byPriority[record.priority], index)
			records[index] = record
		}
	}
	priorities := make([]int, 0, len(byPriority))
	for priority := range byPriority {
		priorities = append(priorities, priority)
	}
	sort.Ints(priorities)
	groups := make([][]api.Message, 0, len(priorities))
	for _, priority := range priorities {
		selected := map[int]bool{}
		for _, index := range byPriority[priority] {
			selected[index] = true
			target := records[index]
			for other, candidate := range records {
				for _, file := range candidate.files {
					for _, addressed := range target.files {
						if file.RootID == addressed.RootID && file.RootPath == addressed.RootPath && policyPathApplies(file.Path, addressed.Path) {
							selected[other] = true
						}
					}
				}
			}
		}
		var group []api.Message
		for index := range messages {
			if selected[index] {
				group = append(group, messages[index])
			}
		}
		groups = append(groups, group)
	}
	return groups
}
