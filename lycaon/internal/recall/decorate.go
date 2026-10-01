package recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/search"
)

// decorate adds attribution, currency, and — on narrow answers — bodies.
func (s *Service) decorate(ctx context.Context, caller Caller, scope Scope, hits []search.Hit) ([]Hit, []string) {
	out := make([]Hit, 0, len(hits))
	inline := len(hits) <= BodyInlineMaxHits
	for _, h := range hits {
		out = append(out, Hit{
			Handle:    h.Handle,
			HitID:     h.ID,
			Kind:      firstNonEmpty(h.Kind, h.HitKind),
			Tool:      strings.TrimSpace(h.Tool),
			SessionID: h.SessionID,
			AgentType: h.AgentType,
			LegID:     h.LegID,
			Mine:      strings.TrimSpace(h.SessionID) == strings.TrimSpace(caller.SessionID),
			TS:        h.TS,
			Path:      h.Path,
			Line:      h.Line,
			URL:       h.URL,
			Snippet:   clampChars(h.Snippet, SnippetMaxChars),
			Untrusted: h.Untrusted,
			Trust:     h.Trust,
			Verified:  h.Verified,
			Currency:  CurrencyUnknown,
		})
	}
	var issues []string
	if inline {
		issues = s.attachBodies(ctx, out)
	}
	s.attachSourceContexts(ctx, hits, out)
	s.attachCurrency(ctx, scope, out)
	return out, issues
}

// attachBodies reads bodies from evidence_records; the projection carries only
// a snippet.
func (s *Service) attachBodies(ctx context.Context, hits []Hit) []string {
	var issues []string
	for i := range hits {
		handle := strings.TrimSpace(hits[i].Handle)
		session := strings.TrimSpace(hits[i].SessionID)
		if handle == "" || session == "" {
			continue
		}
		var projectID, sha string
		err := s.db.QueryRowContext(ctx,
			`SELECT project_id, content_blob_sha256 FROM evidence_records WHERE session_id = ? AND handle = ?`,
			session, handle).Scan(&projectID, &sha)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			issues = append(issues, fmt.Sprintf("%s: load recorded body metadata: %v", handle, err))
			continue
		}
		if sha == "" {
			continue
		}
		raw, err := contentblob.Read(contentblob.StoreFor(s.dataDir, projectID), sha)
		if err != nil {
			issues = append(issues, fmt.Sprintf("%s: read recorded body: %v", handle, err))
			continue
		}
		var body []string
		if err := json.Unmarshal(raw, &body); err != nil {
			issues = append(issues, fmt.Sprintf("%s: decode recorded body: %v", handle, err))
			continue
		}
		if len(body) > BodyMaxLines {
			body = body[:BodyMaxLines]
			hits[i].BodyTruncated = true
		}
		hits[i].Body = body
	}
	return issues
}

// attachCurrency compares path evidence with later source effects.
func (s *Service) attachCurrency(ctx context.Context, scope Scope, hits []Hit) {
	project := strings.TrimSpace(scope.ProjectID)
	if project == "" {
		return
	}
	for i := range hits {
		path := strings.TrimSpace(hits[i].Path)
		observed := strings.TrimSpace(hits[i].TS)
		if path == "" || observed == "" {
			continue
		}
		var op, currentPath, previousPath string
		err := s.db.QueryRowContext(ctx, `
			SELECT e.op, e.path, e.from_path
			FROM source_effects e
			WHERE e.project_id = ? AND (e.path = ? OR e.from_path = ?) AND e.created_ts > ?
			ORDER BY e.ordinal DESC LIMIT 1
		`, project, path, path, observed).Scan(&op, &currentPath, &previousPath)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			hits[i].Currency = CurrencyUnchanged
		case err != nil:
			hits[i].Currency = CurrencyUnknown
		case strings.EqualFold(strings.TrimSpace(op), "delete"),
			strings.EqualFold(strings.TrimSpace(op), "rename") &&
				previousPath == path && currentPath != path:
			hits[i].Currency = CurrencyPathGone
		default:
			hits[i].Currency = CurrencyChanged
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func clampChars(s string, max int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= max {
		return s
	}
	return runeclamp.Clamp(s, max)
}
