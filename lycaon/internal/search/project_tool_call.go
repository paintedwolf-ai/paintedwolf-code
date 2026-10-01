package search

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectToolCalls projects assistant tool invocations into search rows.
func ProjectToolCalls(projectID, sessionID string, msg api.Message) []IndexRow {
	if msg.Role != api.MessageRoleAssistant || len(msg.ToolCalls) == 0 {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	ts := timelayout.Format(msg.CreatedAt)
	rows := make([]IndexRow, 0, len(msg.ToolCalls))
	for i, call := range msg.ToolCalls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			continue
		}
		sourceRef := strings.TrimSpace(call.ID)
		rows = append(rows, IndexRow{
			ID:        RowID(SourceMessage, msg.ID, "toolcall:"+itoa(i)+":"+name),
			ProjectID: projectID,
			Source:    SourceMessage,
			HitKind:   HitKindTool,
			SessionID: sessionID,
			MessageID: msg.ID,
			SourceRef: sourceRef,
			Tool:      name,
			Role:      string(msg.Role),
			Path:      toolCallPath(call.Args),
			Snippet:   toolCallSnippet(name, call.Args),
			TS:        ts,
			Untrusted: ingestion.IsRetrievalTool(name),
		})
	}
	return rows
}

// toolCallPath returns the structured file anchor.
func toolCallPath(args map[string]any) string {
	if raw, ok := args["path"].(string); ok {
		return strings.TrimSpace(raw)
	}
	return ""
}

func toolCallSnippet(name string, args map[string]any) string {
	if len(args) == 0 {
		return name
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return name
	}
	return truncateSnippet(name+" "+string(encoded), 2000)
}
