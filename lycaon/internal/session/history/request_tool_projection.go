package history

import (
	"encoding/json"

	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

// projectRecordedToolBodies preserves active exchanges while moving large bodies behind recall.
func projectRecordedToolBodies(rows []api.Message) []api.Message {
	for i, row := range rows {
		if row.Role != api.MessageRoleTool || row.ToolResult == nil || row.ID == "" || row.Ord <= 0 || len(row.Content) <= 2048 {
			continue
		}
		content, fitted := tooloutput.FitWireJSON(row.Content, 2048)
		if !fitted {
			raw, err := json.Marshal(struct {
				Compacted bool                  `json:"compacted"`
				Outcome   api.ToolResultOutcome `json:"outcome,omitempty"`
				Record    recall.HistoryRequest `json:"record_request"`
			}{true, row.ToolResult.Outcome, recall.HistoryRequest{ThroughOrd: row.Ord, MessageID: row.ID,
				SourceSeq: row.Seq, Format: "record", BodyRunes: 1024}})
			if err != nil {
				continue
			}
			content = string(raw)
		}
		tiers := []api.ContentTrustTier{row.TrustTier}
		for _, part := range api.MessageTextParts(row) {
			tiers = append(tiers, part.TrustTier)
		}
		row.TrustTier = api.FloorTrustTier(tiers...)
		row.Authority = api.ContentAuthorityNone
		row.Content = content
		row.ContentParts = nil
		row.ModelProjected = false
		result := *row.ToolResult
		result.Content = content
		row.ToolResult = &result
		rows[i] = row
	}
	return rows
}
