package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// SummaryQuery selects top-level project chats.
type SummaryQuery struct {
	ProjectID string
	// Archived selects archived chats.
	Archived bool
	// Pinned narrows to pinned (true) or unpinned (false) chats; nil keeps both.
	Pinned *bool
	// TitleQuery matches a case-insensitive title substring.
	TitleQuery string
	Sort       api.SessionListSort
	Order      api.SessionListOrder
	Limit      int
	Cursor     string
	After      *SummaryCursor
}

type SummaryCursor struct {
	Value string `json:"value"`
	ID    string `json:"id"`
}

type SummaryPage struct {
	Sessions []api.SessionSummary
	Total    int
	Next     *SummaryCursor
}

func (q SummaryQuery) effectiveLimit() int {
	if q.Limit <= 0 {
		return api.DefaultSessionListLimit
	}
	if q.Limit > api.MaxSessionListLimit {
		return api.MaxSessionListLimit
	}
	return q.Limit
}

func (q SummaryQuery) sortExpression() string {
	switch q.Sort {
	case api.SessionListSortTitle:
		return "lower(COALESCE(s.title, ''))"
	case api.SessionListSortCreated:
		return "s.created_at"
	case api.SessionListSortPin:
		return pinRankSortExpression
	default:
		return "s.activity_at"
	}
}

// pinRankSortExpression compares ranks as fixed-width text, matching the cursor
// value. The pin sort applies only to pinned chats, so the rank is never null.
const pinRankSortExpression = "printf('%020d', s.pin_rank)"

func pinRankCursorValue(rank int) string {
	return fmt.Sprintf("%020d", rank)
}

// orderClause uses only closed sort values.
func (q SummaryQuery) orderClause() string {
	desc := q.Order != api.SessionListOrderAsc
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	return fmt.Sprintf("%s %s, s.id %s", q.sortExpression(), dir, dir)
}

// escapeLike preserves literal LIKE wildcards.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func sessionSummaryFilter(q SummaryQuery) (string, []any) {
	archived := "s.archived_at IS NULL"
	if q.Archived {
		archived = "s.archived_at IS NOT NULL"
	}
	pinned := ""
	if q.Pinned != nil {
		pinned = "\n\tAND s.pin_rank IS NULL"
		if *q.Pinned {
			pinned = "\n\tAND s.pin_rank IS NOT NULL"
		}
	}
	needle := escapeLike(strings.ToLower(strings.TrimSpace(q.TitleQuery)))
	return `
	s.project_id = ?
	AND s.parent_session_id IS NULL
	AND ` + archived + pinned + `
	AND (? = '' OR lower(COALESCE(s.title, '')) LIKE '%' || ? || '%' ESCAPE '\')`,
		[]any{strings.TrimSpace(q.ProjectID), needle, needle}
}

// ListProjectSummaries returns one chat page and its total.
func (s *SQL) ListProjectSummaries(ctx context.Context, q SummaryQuery) (SummaryPage, error) {
	if err := ctx.Err(); err != nil {
		return SummaryPage{}, err
	}
	filterSQL, filterArgs := sessionSummaryFilter(q)

	var total int
	countSQL := `SELECT COUNT(*) FROM sessions s WHERE` + filterSQL
	if err := s.db.QueryRowContext(ctx, countSQL, filterArgs...).Scan(&total); err != nil {
		return SummaryPage{}, fmt.Errorf("count session summaries: %w", err)
	}

	listWhere := filterSQL
	listArgs := append([]any(nil), filterArgs...)
	if q.After != nil {
		comparison := ">"
		if q.Order != api.SessionListOrderAsc {
			comparison = "<"
		}
		expression := q.sortExpression()
		listWhere += fmt.Sprintf("\n\tAND (%s %s ? OR (%s = ? AND s.id %s ?))",
			expression, comparison, expression, comparison)
		listArgs = append(listArgs, q.After.Value, q.After.Value, q.After.ID)
	}

	// #nosec G202 -- query fragments come from closed package constants.
	listSQL := `
SELECT s.id, s.project_id, s.owner_person_id, s.title, s.posture, s.status, s.archived_at, s.pin_rank,
       s.created_at, s.activity_at,
       (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id) AS message_count
FROM sessions s
WHERE` + listWhere + `
ORDER BY ` + q.orderClause() + `
LIMIT ?`
	limit := q.effectiveLimit()
	listArgs = append(listArgs, limit+1)
	rows, err := s.db.QueryContext(ctx, listSQL, listArgs...)
	if err != nil {
		return SummaryPage{}, fmt.Errorf("list session summaries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []api.SessionSummary{}
	for rows.Next() {
		var (
			row                   api.SessionSummary
			title, arch           sql.NullString
			pin                   sql.NullInt64
			createdAt, activityAt string
			posture, status       string
		)
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.OwnerPersonID, &title, &posture, &status,
			&arch, &pin, &createdAt, &activityAt, &row.MessageCount); err != nil {
			return SummaryPage{}, fmt.Errorf("scan session summary: %w", err)
		}
		row.Title = title.String
		row.Posture = api.SessionPosture(posture)
		row.Status = api.SessionStatus(status)
		if row.CreatedAt, err = db.ParseTime(createdAt); err != nil {
			return SummaryPage{}, err
		}
		if row.ActivityAt, err = db.ParseTime(activityAt); err != nil {
			return SummaryPage{}, err
		}
		if row.ArchivedAt, err = db.TimePtrFromNull(arch); err != nil {
			return SummaryPage{}, err
		}
		if pin.Valid {
			rank := int(pin.Int64)
			row.PinRank = &rank
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return SummaryPage{}, err
	}
	page := SummaryPage{Sessions: out, Total: total}
	if len(page.Sessions) > limit {
		page.Sessions = page.Sessions[:limit]
		cursor := summaryCursor(page.Sessions[len(page.Sessions)-1], q.Sort)
		page.Next = &cursor
	}
	return page, nil
}

// ListProjectSummaries matches SQL paging in memory.
func (s *Memory) ListProjectSummaries(ctx context.Context, q SummaryQuery) (SummaryPage, error) {
	if err := ctx.Err(); err != nil {
		return SummaryPage{}, err
	}
	projectID := strings.TrimSpace(q.ProjectID)
	needle := strings.ToLower(strings.TrimSpace(q.TitleQuery))

	s.mu.RLock()
	matched := []api.SessionSummary{}
	for id, sess := range s.sessions {
		if strings.TrimSpace(sess.ProjectID) != projectID {
			continue
		}
		if strings.TrimSpace(sess.ParentSessionID) != "" {
			continue
		}
		if (sess.ArchivedAt != nil) != q.Archived {
			continue
		}
		if q.Pinned != nil && (sess.PinRank != nil) != *q.Pinned {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(sess.Title), needle) {
			continue
		}
		matched = append(matched, api.SessionSummary{
			ID:            sess.ID,
			ProjectID:     sess.ProjectID,
			OwnerPersonID: sess.OwnerPersonID,
			Title:         sess.Title,
			Posture:       sess.Posture,
			Status:        sess.Status,
			ArchivedAt:    sess.ArchivedAt,
			PinRank:       sess.PinRank,
			MessageCount:  len(s.messages[id]),
			CreatedAt:     sess.CreatedAt,
			ActivityAt:    sess.ActivityAt,
		})
	}
	s.mu.RUnlock()

	sort.SliceStable(matched, func(i, j int) bool {
		comparison := compareSummary(matched[i], summaryCursor(matched[j], q.Sort), q.Sort)
		if q.Order != api.SessionListOrderAsc {
			return comparison > 0
		}
		return comparison < 0
	})

	total := len(matched)
	start := 0
	if q.After != nil {
		for start < len(matched) {
			comparison := compareSummary(matched[start], *q.After, q.Sort)
			if q.Order == api.SessionListOrderAsc && comparison > 0 ||
				q.Order != api.SessionListOrderAsc && comparison < 0 {
				break
			}
			start++
		}
	}
	end := start + q.effectiveLimit()
	if end > total {
		end = total
	}
	page := SummaryPage{Sessions: matched[start:end], Total: total}
	if end < total && len(page.Sessions) > 0 {
		cursor := summaryCursor(page.Sessions[len(page.Sessions)-1], q.Sort)
		page.Next = &cursor
	}
	return page, nil
}

func summaryCursor(row api.SessionSummary, sortBy api.SessionListSort) SummaryCursor {
	value := db.FormatTime(row.ActivityAt)
	switch sortBy {
	case api.SessionListSortTitle:
		value = strings.ToLower(row.Title)
	case api.SessionListSortCreated:
		value = db.FormatTime(row.CreatedAt)
	case api.SessionListSortPin:
		if row.PinRank != nil {
			value = pinRankCursorValue(*row.PinRank)
		}
	case api.SessionListSortActivity:
	}
	return SummaryCursor{Value: value, ID: row.ID}
}

func compareSummary(row api.SessionSummary, cursor SummaryCursor, sortBy api.SessionListSort) int {
	value := summaryCursor(row, sortBy).Value
	if comparison := strings.Compare(value, cursor.Value); comparison != 0 {
		return comparison
	}
	return strings.Compare(row.ID, cursor.ID)
}
