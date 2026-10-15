package recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/db"
)

const historyBodyRunes = 4096

// ErrHistoryCoordinates identifies an invalid recovery request.
type ErrHistoryCoordinates struct{ Reason string }

func (e *ErrHistoryCoordinates) Error() string { return "recall: " + e.Reason }

const historyVisible = `visibility = 'transcript' AND (kind != 'draft' OR draft_status = 'committed')`

const historyText = `COALESCE(NULLIF(content,''),json_extract(NULLIF(tool_result_json,''),'$.content'),'')`

const historyPreview = `COALESCE(NULLIF(` + historyText + `,''),NULLIF(` + historyCalls + `,'[]'),'')`

const historySearchEnvelope = `COALESCE(evidence_handles_json,'') || ' ' || COALESCE(` + historyCalls + `,'') || ' ' || COALESCE(json_remove(NULLIF(tool_result_json,''),'$.content'),'')`

// HistoryRequest addresses recorded rows in the caller's own session.
type HistoryRequest struct {
	Format     string `json:"format,omitempty"`
	BodyRunes  int    `json:"body_runes,omitempty"`
	Search     string `json:"search,omitempty"`
	AfterOrd   int64  `json:"after_ord"`
	ThroughOrd int64  `json:"through_ord"`
	SourceSeq  int64  `json:"source_seq,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
	Offset     int    `json:"offset,omitempty"`
}

// HistoryPage is an ordinal index or a bounded recorded body.
type HistoryPage struct {
	Format        string         `json:"format,omitempty"`
	Entries       []HistoryEntry `json:"entries"`
	NextAfterOrd  int64          `json:"next_after_ord,omitempty"`
	NextOffset    *int           `json:"next_offset,omitempty"`
	ThroughOrd    int64          `json:"through_ord"`
	SourceChanged bool           `json:"source_changed"`
	Recorded      bool           `json:"recorded"`
}

// HistoryEntry retains observation coordinates without promoting their authority.
type HistoryEntry struct {
	Record    *HistoryRequest `json:"record_request,omitempty"`
	MessageID string          `json:"message_id"`
	Ord       int64           `json:"ord"`
	Role      string          `json:"role"`
	Handles   []string        `json:"handles,omitempty"`
	Content   string          `json:"content"`
	Runes     int             `json:"runes"`
	Offset    int             `json:"offset,omitempty"`
	Seq       int64           `json:"seq"`
}

func (s *Service) history(ctx context.Context, req Request) (*Result, error) {
	h := req.History
	if (h.Format != "" && h.Format != "text" && h.Format != "record") || (h.Format == "record" && h.MessageID == "") || h.BodyRunes < 0 || h.BodyRunes > historyBodyRunes || h.Offset == math.MaxInt {
		return nil, &ErrHistoryCoordinates{Reason: "invalid history body format or window"}
	}
	if len([]rune(h.Search)) > 256 || len(strings.Fields(h.Search)) > 8 || (h.MessageID != "" && strings.TrimSpace(h.Search) != "") {
		return nil, &ErrHistoryCoordinates{Reason: "history search must be bounded and separate from a body request"}
	}
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
		if errors.Is(err, sql.ErrNoRows) {
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
		var present bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE session_id = ? AND "+historyVisible+" LIMIT 1)", req.Caller.SessionID).Scan(&present); err != nil {
			return nil, err
		}
		if !present {
			out.Resolution = ResolutionScopeEmpty
		}
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
	page.Format = "index"
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	query := `SELECT id, ord, role, substr(` + historyPreview + `,1,120), length(` + historyPreview + `), COALESCE(evidence_handles_json,'[]'), seq
  FROM messages WHERE session_id = ? AND ord > ? AND ord <= ? AND ` + historyVisible + ` ORDER BY ord LIMIT ?`
	args := []any{session, h.AfterOrd, h.ThroughOrd, limit + 1}
	if terms := strings.Fields(h.Search); len(terms) > 0 {
		ftsTerms := make([]string, len(terms))
		lowerTerms := make([]string, len(terms))
		for i, term := range terms {
			ftsTerms[i] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
			lowerTerms[i] = strings.ToLower(term)
		}
		termsJSON, _ := json.Marshal(lowerTerms)
		query = `SELECT id, ord, role, substr(` + historyPreview + `,1,120), length(` + historyPreview + `), COALESCE(evidence_handles_json,'[]'), seq
  FROM messages WHERE session_id = ? AND ord > ? AND ord <= ? AND ` + historyVisible + `
  AND (rowid IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?) OR NOT EXISTS (SELECT 1 FROM json_each(?) WHERE instr(lower(` + historyPreview + ` || ' ' || ` + historySearchEnvelope + `), value) = 0))
  ORDER BY ord LIMIT ?`
		args = []any{session, h.AfterOrd, h.ThroughOrd, strings.Join(ftsTerms, " AND "), string(termsJSON), limit + 1}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
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
		entry.Record = &HistoryRequest{Format: "record", ThroughOrd: h.ThroughOrd, MessageID: entry.MessageID,
			SourceSeq: entry.Seq, BodyRunes: h.BodyRunes}
		page.Entries = append(page.Entries, entry)
	}
	return rows.Err()
}

func historyBody(ctx context.Context, tx *sql.Tx, session string, h *HistoryRequest, page *HistoryPage) error {
	text := historyText
	page.Format = "text"
	if h.Format == "record" {
		text, page.Format = historyRecord, "record"
	}
	width := historyBodyRunes
	if h.BodyRunes > 0 {
		width = h.BodyRunes
	}
	row := tx.QueryRowContext(ctx, `SELECT id, ord, role, substr(`+text+`,?,?), length(`+text+`), COALESCE(evidence_handles_json,'[]'), seq
  FROM messages WHERE session_id = ? AND id = ? AND ord > ? AND ord <= ? AND `+historyVisible, h.Offset+1, width, session, h.MessageID, h.AfterOrd, h.ThroughOrd)
	entry, err := scanHistoryEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
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
	if err := row.Scan(&entry.MessageID, &entry.Ord, &entry.Role, &entry.Content, &entry.Runes, &handles, &entry.Seq); err != nil {
		return entry, err
	}
	if err := json.Unmarshal([]byte(handles), &entry.Handles); err != nil {
		return entry, fmt.Errorf("recall history handles: %w", err)
	}
	return entry, nil
}
