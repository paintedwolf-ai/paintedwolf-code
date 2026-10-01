package search

import (
	"strings"

	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectDraftVersion indexes a rejected coordinator draft snapshot.
func ProjectDraftVersion(projectID, sessionID, slotID string, version api.DraftVersion) []IndexRow {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || strings.TrimSpace(version.OutcomeCode) == "" {
		return nil
	}
	body := strings.TrimSpace(version.Body)
	if body == "" {
		return nil
	}
	id := slotID + ":" + itoa(version.VersionIndex)
	return []IndexRow{{
		ID:        RowID(SourceMessage, id, "draft_version"),
		ProjectID: projectID,
		Source:    SourceMessage,
		HitKind:   HitKindMessage,
		SessionID: sessionID,
		MessageID: slotID,
		SourceRef: slotID,
		Kind:      "draft_version",
		Role:      string(api.MessageKindDraft),
		Snippet:   truncateSnippet(body, 2000),
		HintCode:  version.OutcomeCode,
		TS:        timelayout.Format(version.CreatedAt),
	}}
}
