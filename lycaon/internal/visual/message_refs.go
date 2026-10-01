package visual

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// MessageRefs derives every artifact claim made by one transcript row.
func MessageRefs(projectID, sessionID string, msg api.Message) []RefWrite {
	projectID = strings.TrimSpace(projectID)
	messageID := strings.TrimSpace(msg.ID)
	if projectID == "" || messageID == "" {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	base := RefWrite{ProjectID: projectID, MessageID: messageID, SessionID: sessionID}

	listKind := api.ArtifactReferenceKindMessagePresent
	if msg.Role == api.MessageRoleUser {
		listKind = api.ArtifactReferenceKindMessageAttachment
	}

	var out []RefWrite
	add := func(kind api.ArtifactReferenceKind, id, toolCallID string) {
		if id = strings.TrimSpace(id); id == "" {
			return
		}
		ref := base
		ref.Kind = kind
		ref.ArtifactID = id
		ref.ToolCallID = strings.TrimSpace(toolCallID)
		out = append(out, ref)
	}

	for _, id := range msg.ArtifactIDs {
		add(listKind, id, "")
	}
	if tr := msg.ToolResult; tr != nil && tr.Visual != nil {
		add(api.ArtifactReferenceKindToolResult, tr.Visual.ID, tr.ToolCallID)
	}
	if fb := msg.WorkflowFeedback; fb != nil {
		add(api.ArtifactReferenceKindMessageAttachment, fb.ArtifactID, "")
		for _, id := range fb.ArtifactIDs {
			add(api.ArtifactReferenceKindMessageAttachment, id, "")
		}
	}
	return out
}
