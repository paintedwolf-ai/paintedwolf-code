package recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/db"
)

const historyBodyRunes = 4096

// ErrHistoryCoordinates identifies an invalid recovery request.
type ErrHistoryCoordinates struct{ Reason string }

func (e *ErrHistoryCoordinates) Error() string { return "recall: " + e.Reason }

const historyText = `COALESCE(NULLIF(content,''),json_extract(NULLIF(tool_result_json,''),'$.content'),'')`

// HistoryRequest addresses recorded rows in the caller's own session.
type HistoryRequest struct {
	AfterOrd   int64  `json:"after_ord"`
	ThroughOrd int64  `json:"through_ord"`
	SourceSeq  int64  `json:"source_seq,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
	Offset     int    `json:"offset,omitempty"`
}

// HistoryPage is an ordinal index or a bounded recorded body.
type HistoryPage struct {
	Entries       []HistoryEntry `json:"entries"`
	NextAfterOrd  int64          `json:"next_after_ord,omitempty"`
	NextOffset    *int           `json:"next_offset,omitempty"`
	ThroughOrd    int64          `json:"through_ord"`
	SourceChanged bool           `json:"source_changed"`
	Recorded      bool           `json:"recorded"`
}

// HistoryEntry retains observation coordinates without promoting their authority.
type HistoryEntry struct {
	ID      string   `json:"id"`
	Ord     int64    `json:"ord"`
	Role    string   `json:"role"`
	Handles []string `json:"handles,omitempty"`
	Content string   `json:"content"`
	Runes   int      `json:"runes"`
	Offset  int      `json:"offset,omitempty"`
	Seq     int64    `json:"seq"`
}

func (s *Service) history(ctx context.Context, req Request) (*Result, error) {
	h := req.History
	if h.ThroughOrd <= 0 || h.AfterOrd < 0 || h.AfterOrd >= h.ThroughOrd || h.Offset < 0 || h.SourceSeq < 0 || strings.TrimSpace(req.Caller.SessionID) == "" {
		return nil, &ErrHistoryCoordinates{Reason: "invalid history coordinates"}
	}
	if req.Widen != WidenDefault || strings.TrimSpace(req.Query) != "" {
		return nil, &ErrHistoryCoordinates{Reason: "history cannot be combined with query or widen"}
	}
	var tx *sql.Tx
	var err error
	if reader, ok := s.db.(db.ReadHandle); ok {
		tx, err = reader.BeginReadTx(ctx)
	} else {
		tx, err = s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var project string
	if err := tx.QueryRowContext(ctx, "SELECT project_id FROM sessions WHERE id = ?", req.Caller.SessionID).Scan(&project); err != nil {
		if err == sql.ErrNoRows {
			return &Result{Resolution: ResolutionRecordDeleted, ScopeStated: "this session only", History: &HistoryPage{Recorded: true}}, nil
		}
		return nil, err
	}
	if project != req.Caller.ProjectID {
		return nil, &ErrScopeDenied{Role: req.Caller.Role(), Widen: WidenDefault}
	}
	page := &HistoryPage{ThroughOrd: h.ThroughOrd, Recorded: true, Entries: []HistoryEntry{}}
	if h.MessageID != "" {
		err = historyBody(ctx, tx, req.Caller.SessionID, h, page)
	} else {
		err = historyIndex(ctx, tx, req.Caller.SessionID, h, req.Limit, page)
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range page.Entries {
		if h.SourceSeq > 0 && entry.Seq > h.SourceSeq {
			page.SourceChanged = true
		}
	}
	out := &Result{Resolution: ResolutionMatched, ScopeStated: "this session only", History: page,
		Count:     Count{Value: len(page.Entries), Relation: RelationExact},
		Truncated: page.NextAfterOrd > 0 || page.NextOffset != nil}
	if len(page.Entries) == 0 {
		out.Resolution = ResolutionNoMatchInScope
	}
	if out.Truncated {
		out.Count.Relation = RelationAtLeast
	}
	out.Receipt = Receipt{Tool: ToolName, PathsTouched: len(page.Entries), Truncated: out.Truncated}
	for _, entry := range page.Entries {
		out.Receipt.BytesReturned += len(entry.Content)
	}
	return out, nil
}

func historyIndex(ctx context.Context, tx *sql.Tx, session string, h *HistoryRequest, limit int, page *HistoryPage) error {
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	rows, err := tx.QueryContext(ctx, `SELECT id, ord, role, substr(`+historyText+`,1,120), length(`+historyText+`), COALESCE(evidence_handles_json,'[]'), seq
		FROM messages WHERE session_id = ? AND ord > ? AND ord <= ? ORDER BY ord LIMIT ?`, session, h.AfterOrd, h.ThroughOrd, limit+1)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if len(page.Entries) == limit {
			page.NextAfterOrd = page.Entries[len(page.Entries)-1].Ord
			break
		}
		entry, err := scanHistoryEntry(rows)
		if err != nil {
			return err
		}
		page.Entries = append(page.Entries, entry)
	}
	return rows.Err()
}

func historyBody(ctx context.Context, tx *sql.Tx, session string, h *HistoryRequest, page *HistoryPage) error {
	row := tx.QueryRowContext(ctx, `SELECT id, ord, role, substr(`+historyText+`,?,?), length(`+historyText+`), COALESCE(evidence_handles_json,'[]'), seq
		FROM messages WHERE session_id = ? AND id = ? AND ord > ? AND ord <= ?`, h.Offset+1, historyBodyRunes, session, h.MessageID, h.AfterOrd, h.ThroughOrd)
	entry, err := scanHistoryEntry(row)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	entry.Offset = h.Offset
	page.Entries = append(page.Entries, entry)
	if next := h.Offset + utf8.RuneCountInString(entry.Content); next < entry.Runes {
		page.NextOffset = &next
	}
	return nil
}

func scanHistoryEntry(row interface{ Scan(...any) error }) (HistoryEntry, error) {
	var entry HistoryEntry
	var handles string
	if err := row.Scan(&entry.ID, &entry.Ord, &entry.Role, &entry.Content, &entry.Runes, &handles, &entry.Seq); err != nil {
		return entry, err
	}
	if err := json.Unmarshal([]byte(handles), &entry.Handles); err != nil {
		return entry, fmt.Errorf("recall history handles: %w", err)
	}
	return entry, nil
}
