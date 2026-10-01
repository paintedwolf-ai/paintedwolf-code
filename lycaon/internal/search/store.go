package search

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Store maintains evidence_index projections inside the caller's transaction.
// FTS5 virtual tables sync via schema.sql triggers on messages and evidence_index.
type Store struct{}

// NewStore returns a projection store.
func NewStore() *Store {
	return &Store{}
}

// UpsertRows stamps session ancestry before retention can remove parent links.
func (s *Store) UpsertRows(ctx context.Context, tx *sql.Tx, rows []IndexRow) error {
	roots := make(map[string]string, 1)
	for _, row := range rows {
		root, err := resolveRootSession(ctx, tx, roots, row.SessionID)
		if err != nil {
			return err
		}
		row.RootSessionID = root
		if err := upsertEvidenceRow(ctx, tx, row); err != nil {
			return err
		}
	}
	return nil
}

func resolveRootSession(ctx context.Context, tx *sql.Tx, memo map[string]string, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", nil
	}
	if root, ok := memo[sessionID]; ok {
		return root, nil
	}
	root, err := rootSessionIDTx(ctx, tx, sessionID)
	if err != nil {
		return "", err
	}
	memo[sessionID] = root
	return root, nil
}

// clearMessageEvidence removes projections rebuilt by a message update.
func (s *Store) clearMessageEvidence(ctx context.Context, tx *sql.Tx, messageID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM evidence_index WHERE message_id = ?`, messageID)
	return err
}

func upsertEvidenceRow(ctx context.Context, tx *sql.Tx, row IndexRow) error {
	hitKind := strings.TrimSpace(row.HitKind)
	if hitKind == "" {
		return fmt.Errorf("upsert evidence_index: hit_kind is required")
	}
	verified := sql.NullInt64{}
	if row.Verified != nil {
		verified = sql.NullInt64{Int64: boolToInt(*row.Verified), Valid: true}
	}
	line := sql.NullInt64{}
	if row.Line > 0 {
		line = sql.NullInt64{Int64: int64(row.Line), Valid: true}
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO evidence_index (
			id, project_id, source, hit_kind, session_id, root_session_id, message_id, source_ref, leg_id,
			handle, tool, kind, shape, role, path, line, url, snippet, verified, hint_code,
			check_id, trust, truncated, ts, workflow_run_id, verdict, untrusted
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			project_id = excluded.project_id,
			source = excluded.source,
			hit_kind = excluded.hit_kind,
			session_id = excluded.session_id,
			root_session_id = excluded.root_session_id,
			message_id = excluded.message_id,
			source_ref = excluded.source_ref,
			leg_id = excluded.leg_id,
			handle = excluded.handle,
			tool = excluded.tool,
			kind = excluded.kind,
			shape = excluded.shape,
			role = excluded.role,
			path = excluded.path,
			line = excluded.line,
			url = excluded.url,
			snippet = excluded.snippet,
			verified = excluded.verified,
			hint_code = excluded.hint_code,
			check_id = excluded.check_id,
			trust = excluded.trust,
			truncated = excluded.truncated,
			ts = excluded.ts,
			workflow_run_id = excluded.workflow_run_id,
			verdict = excluded.verdict,
			untrusted = excluded.untrusted
	`, row.ID, row.ProjectID, row.Source, hitKind, nullString(row.SessionID),
		nullString(row.RootSessionID),
		nullString(row.MessageID), nullString(row.SourceRef), nullString(row.LegID),
		nullString(row.Handle), nullString(row.Tool), nullString(row.Kind), nullString(row.Shape), nullString(row.Role),
		nullString(row.Path), line, nullString(row.URL), nullString(row.Snippet), verified,
		nullString(row.HintCode), nullString(row.CheckID), nullString(row.Trust),
		boolToInt(row.Truncated), nullString(row.TS), nullString(row.WorkflowRunID), nullString(row.Verdict),
		boolToInt(row.Untrusted))
	if err != nil {
		return fmt.Errorf("upsert evidence_index: %w", err)
	}
	return nil
}

func nullString(s string) sql.NullString {
	s = strings.TrimSpace(s)
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
