package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// ErrEvidenceNotFound means no evidence_index row matched the lookup keys.
var ErrEvidenceNotFound = errors.New("evidence index row not found")

// EvidenceSnippet is the host-managed body re-served for a search-hit reference.
type EvidenceSnippet struct {
	MessageID string
	HitKind   string
	SourceRef string
	Snippet   string
	SessionID string
	// Untrusted is the taint recorded when this content was indexed. It travels
	// with the snippet so a session that re-serves it inherits the taint.
	Untrusted bool
}

// LookupBySourceRef uses stored coordinates, with optional kind and session filters.
func LookupBySourceRef(ctx context.Context, database db.DBTX, projectID, sourceRef, hitKind, sessionID string) (EvidenceSnippet, error) {
	if database == nil {
		return EvidenceSnippet{}, ErrEvidenceNotFound
	}
	projectID = strings.TrimSpace(projectID)
	sourceRef = strings.TrimSpace(sourceRef)
	if projectID == "" || sourceRef == "" {
		return EvidenceSnippet{}, ErrEvidenceNotFound
	}
	hitKind = strings.TrimSpace(hitKind)
	sessionID = strings.TrimSpace(sessionID)

	query := `
		SELECT COALESCE(hit_kind, ''), COALESCE(source_ref, ''), COALESCE(snippet, ''), COALESCE(session_id, ''), COALESCE(untrusted, 0), COALESCE(message_id, '')
		FROM evidence_index
		WHERE project_id = ? AND source_ref = ?`
	args := []any{projectID, sourceRef}
	if hitKind != "" {
		query += ` AND hit_kind = ?`
		args = append(args, hitKind)
	}
	if sessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, sessionID)
	}
	// Ambiguous coordinates prefer untrusted content, then newest timestamp and ID.
	query += ` ORDER BY COALESCE(untrusted, 0) DESC, COALESCE(ts, '') DESC, id DESC LIMIT 1`

	var out EvidenceSnippet
	var untrusted int
	err := database.QueryRowContext(ctx, query, args...).Scan(&out.HitKind, &out.SourceRef, &out.Snippet, &out.SessionID, &untrusted, &out.MessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return EvidenceSnippet{}, ErrEvidenceNotFound
	}
	if err != nil {
		return EvidenceSnippet{}, fmt.Errorf("evidence_index lookup: %w", err)
	}
	out.Untrusted = untrusted != 0
	return out, nil
}
