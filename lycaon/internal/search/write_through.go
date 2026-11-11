package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

var defaultStore = NewStore()

// SyncMessageWriteThrough projects one message write into evidence_index.
func SyncMessageWriteThrough(ctx context.Context, tx *sql.Tx, projectID, sessionID string, msg api.Message) error {
	if err := defaultStore.clearMessageEvidence(ctx, tx, msg.ID); err != nil {
		return err
	}
	toolName := ""
	if msg.ToolResult != nil {
		toolName = msg.ToolResult.Tool
	}
	rows := ProjectMessage(projectID, sessionID, msg)
	rows = append(rows, ProjectMessageText(projectID, sessionID, msg)...)
	rows = append(rows, ProjectToolCalls(projectID, sessionID, msg)...)
	rows = append(rows, ProjectToolMessage(projectID, sessionID, msg, toolName)...)
	rows = append(rows, ProjectLifecycleEvidence(projectID, sessionID, msg)...)
	if err := enrichRowsFromEvidenceRecords(ctx, tx, rows); err != nil {
		return err
	}
	return defaultStore.UpsertRows(ctx, tx, rows)
}

// enrichRowsFromEvidenceRecords backfills path, line, tool, kind, shape, and trust
// on handle-bearing rows from the canonical evidence_records row in the same transaction.
func enrichRowsFromEvidenceRecords(ctx context.Context, tx *sql.Tx, rows []IndexRow) error {
	for i := range rows {
		sessionID := strings.TrimSpace(rows[i].SessionID)
		handle := strings.TrimSpace(rows[i].Handle)
		if sessionID == "" || handle == "" {
			continue
		}
		var kind, shape, fidelity, sourceTool, path, url, lineRanges string
		var marksUntrusted, truncated int64
		err := tx.QueryRowContext(ctx, `
			SELECT kind, shape, fidelity, source_tool, marks_untrusted, path, url, line_ranges, truncated
			FROM evidence_records
			WHERE session_id = ? AND handle = ?
		`, sessionID, handle).Scan(&kind, &shape, &fidelity, &sourceTool, &marksUntrusted, &path, &url, &lineRanges, &truncated)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("enrich evidence_index row %s from evidence_records: %w", handle, err)
		}
		if strings.TrimSpace(rows[i].Path) == "" && strings.TrimSpace(path) != "" {
			rows[i].Path = cleanToolPath(path)
		} else if rows[i].Path != "" {
			rows[i].Path = cleanToolPath(rows[i].Path)
		}
		if rows[i].Line <= 0 {
			rows[i].Line = firstEvidenceRecordLine(lineRanges)
		}
		if strings.TrimSpace(rows[i].Tool) == "" && strings.TrimSpace(sourceTool) != "" {
			rows[i].Tool = strings.TrimSpace(strings.ToLower(sourceTool))
		}
		if (rows[i].Kind == "" || rows[i].Kind == HitKindEvidence || rows[i].Kind == HitKindTool) && strings.TrimSpace(kind) != "" {
			rows[i].Kind = strings.TrimSpace(kind)
		}
		if rows[i].Shape == "" && strings.TrimSpace(shape) != "" {
			rows[i].Shape = strings.TrimSpace(shape)
		}
		if strings.TrimSpace(rows[i].Trust) == "" && strings.TrimSpace(fidelity) != "" {
			rows[i].Trust = strings.TrimSpace(fidelity)
		}
		if strings.TrimSpace(rows[i].URL) == "" && strings.TrimSpace(url) != "" {
			rows[i].URL = strings.TrimSpace(url)
		}
		if truncated != 0 {
			rows[i].Truncated = true
		}
		if marksUntrusted != 0 {
			rows[i].Untrusted = true
		}
	}
	return nil
}

func firstEvidenceRecordLine(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	var payload struct {
		Ranges []evidence.LineRange `json:"ranges"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil || len(payload.Ranges) == 0 {
		return 0
	}
	if payload.Ranges[0].Start > 0 {
		return payload.Ranges[0].Start
	}
	return 0
}

// rootSessionIDTx walks parent links to the top of the session tree.
func rootSessionIDTx(ctx context.Context, tx *sql.Tx, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", nil
	}
	var root string
	err := tx.QueryRowContext(ctx, `
		WITH RECURSIVE lineage(id, parent_id) AS (
		    SELECT sessions.id, sessions.parent_session_id FROM sessions WHERE sessions.id = ?
		    UNION ALL
		    SELECT s.id, s.parent_session_id FROM sessions s
		    JOIN lineage ON lineage.parent_id = s.id
		)
		SELECT lineage.id FROM lineage WHERE lineage.parent_id IS NULL LIMIT 1
	`, sessionID).Scan(&root)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A row indexed for a session the store does not know is its own root.
		return sessionID, nil
	case err != nil:
		return "", fmt.Errorf("resolve root session: %w", err)
	}
	return root, nil
}

// SyncDraftVersionWriteThrough projects one draft_versions row into evidence_index.
func SyncDraftVersionWriteThrough(ctx context.Context, tx *sql.Tx, projectID, sessionID, slotID string, version api.DraftVersion) error {
	rows := ProjectDraftVersion(projectID, sessionID, slotID, version)
	return defaultStore.UpsertRows(ctx, tx, rows)
}

// SyncUntrustedLedgerWriteThrough projects ledger-only trust markers.
func SyncUntrustedLedgerWriteThrough(ctx context.Context, tx *sql.Tx, projectID, sessionID string, rec evidence.Record) error {
	rows := ProjectUntrustedLedgerRecord(projectID, sessionID, rec)
	return defaultStore.UpsertRows(ctx, tx, rows)
}
