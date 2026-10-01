package search

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

var webToolNames = map[string]struct{}{
	"web_search": {},
	"fetch_url":  {},
}

// ProjectToolMessage projects visible tool results into search rows.
func ProjectToolMessage(projectID, sessionID string, msg api.Message, toolName string) []IndexRow {
	if msg.Role != api.MessageRoleTool {
		return nil
	}
	if msg.ToolResult != nil && msg.ToolResult.Outcome != "" && msg.ToolResult.Outcome != api.ToolResultOutcomeCompleted {
		if api.IsInternalTranscriptMessage(msg) {
			return nil
		}
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	content := strings.TrimSpace(msg.Content)
	if msg.ToolResult != nil && strings.TrimSpace(msg.ToolResult.Content) != "" {
		content = strings.TrimSpace(msg.ToolResult.Content)
	}
	if content == "" {
		return nil
	}
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	ts := timelayout.Format(msg.CreatedAt)
	revealRef := ""
	if msg.ToolResult != nil {
		revealRef = strings.TrimSpace(msg.ToolResult.ToolCallID)
	}
	if !isWebTool(toolName) {
		rows := rawToolResultRows(projectID, sessionID, msg, toolName, revealRef, ts, content)
		// Index each observed egress host separately.
		return append(rows, egressObservationRows(projectID, sessionID, msg.ID, revealRef, ts, content)...)
	}
	rows := webObservationRows(projectID, sessionID, msg.ID, revealRef, ts, toolName, content)
	for i := range rows {
		rows[i].Tool = toolName
	}
	if len(rows) == 0 {
		rows = []IndexRow{{
			ID:        RowID(SourceTool, msg.ID, "web:body"),
			ProjectID: projectID,
			Source:    SourceTool,
			HitKind:   HitKindWeb,
			SessionID: sessionID,
			MessageID: msg.ID,
			SourceRef: revealRef,
			Tool:      toolName,
			Kind:      HitKindWeb,
			Role:      string(msg.Role),
			Snippet:   truncateSnippet(content, 2000),
			Trust:     "structured",
			TS:        ts,
		}}
	}
	return markRowsUntrusted(rows, toolName)
}

// rawToolResultRows indexes a non-web tool result as raw text, one row per
// evidence handle the result minted (`recall handle:<h>` and the recorded-body
// lookup key on it). A result that minted none is still indexed under its tool.
func rawToolResultRows(projectID, sessionID string, msg api.Message, toolName, revealRef, ts, content string) []IndexRow {
	handles := make([]string, 0, len(msg.EvidenceHandles))
	for _, h := range msg.EvidenceHandles {
		if h = strings.TrimSpace(h); h != "" {
			handles = append(handles, h)
		}
	}
	if len(handles) == 0 {
		handles = append(handles, "")
	}
	rows := make([]IndexRow, 0, len(handles))
	for _, handle := range handles {
		suffix := "result"
		if handle != "" {
			suffix = "result:" + handle
		}
		rows = append(rows, IndexRow{
			ID:        RowID(SourceTool, msg.ID, suffix),
			ProjectID: projectID,
			Source:    SourceTool,
			HitKind:   HitKindTool,
			SessionID: sessionID,
			MessageID: msg.ID,
			SourceRef: revealRef,
			Handle:    handle,
			Tool:      toolName,
			Shape:     "raw",
			Role:      string(api.MessageRoleTool),
			Snippet:   truncateSnippet(content, 2000),
			TS:        ts,
			Untrusted: ingestion.IsRetrievalTool(toolName),
		})
	}
	return rows
}

// egressObservationRows indexes observed command destinations.
func egressObservationRows(projectID, sessionID, messageID, revealRef, ts, content string) []IndexRow {
	var payload struct {
		Network []struct {
			Host    string `json:"host"`
			Allowed bool   `json:"allowed"`
		} `json:"network"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil
	}
	out := make([]IndexRow, 0, len(payload.Network))
	for i, h := range payload.Network {
		host := strings.TrimSpace(h.Host)
		if host == "" {
			continue
		}
		verb := "connected to"
		if !h.Allowed {
			verb = "blocked from"
		}
		out = append(out, IndexRow{
			ID:        RowID(SourceTool, messageID, rowSuffix("egress", i, host)),
			ProjectID: projectID,
			Source:    SourceTool,
			HitKind:   HitKindNetwork,
			SessionID: sessionID,
			MessageID: messageID,
			SourceRef: revealRef,
			Kind:      HitKindNetwork,
			Role:      string(api.MessageRoleTool),
			URL:       host,
			Snippet:   verb + " " + host,
			Trust:     "structured",
			TS:        ts,
		})
	}
	return out
}

func isWebTool(toolName string) bool {
	_, ok := webToolNames[toolName]
	return ok
}

type webHit struct {
	Title   string
	URL     string
	Snippet string
}

func webObservationRows(projectID, sessionID, messageID, revealRef, ts, toolName, content string) []IndexRow {
	switch toolName {
	case "web_search":
		var payload struct {
			Results []webHit `json:"results"`
		}
		if err := json.Unmarshal([]byte(content), &payload); err != nil {
			return nil
		}
		out := make([]IndexRow, 0, len(payload.Results))
		for i, hit := range payload.Results {
			url := strings.TrimSpace(hit.URL)
			if url == "" {
				continue
			}
			snippet := firstNonEmpty(hit.Snippet, hit.Title)
			out = append(out, IndexRow{
				ID:        RowID(SourceTool, messageID, rowSuffix("web_hit", i, url)),
				ProjectID: projectID,
				Source:    SourceTool,
				HitKind:   HitKindWeb,
				SessionID: sessionID,
				MessageID: messageID,
				SourceRef: revealRef,
				Kind:      HitKindWeb,
				Role:      string(api.MessageRoleTool),
				URL:       url,
				Snippet:   snippet,
				Trust:     "structured",
				TS:        ts,
			})
		}
		return out
	case "fetch_url":
		url := extractFetchURL(content)
		if url == "" {
			return nil
		}
		return []IndexRow{{
			ID:        RowID(SourceTool, messageID, "fetch:"+url),
			ProjectID: projectID,
			Source:    SourceTool,
			HitKind:   HitKindWeb,
			SessionID: sessionID,
			MessageID: messageID,
			SourceRef: revealRef,
			Kind:      HitKindWeb,
			Role:      string(api.MessageRoleTool),
			URL:       url,
			Snippet:   truncateSnippet(content, 2000),
			Trust:     "structured",
			TS:        ts,
		}}
	default:
		return nil
	}
}

func extractFetchURL(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return strings.Fields(line)[0]
		}
	}
	return ""
}

func truncateSnippet(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	// Preserve a valid UTF-8 boundary.
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func markRowsUntrusted(rows []IndexRow, toolName string) []IndexRow {
	if !ingestion.IsRetrievalTool(toolName) {
		return rows
	}
	for i := range rows {
		rows[i].Untrusted = true
	}
	return rows
}
