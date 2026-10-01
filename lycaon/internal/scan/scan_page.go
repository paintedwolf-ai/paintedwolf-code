package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/pkg/api"
)

var scanPages = pagecursor.For[scanPageCursor]("code_scans")

// ErrInvalidScanPageCursor wraps the pagecursor error that refused a cursor.
var ErrInvalidScanPageCursor = errors.New("invalid scan page cursor")

// PageQueryError names the query parameter outside the closed sort vocabulary.
type PageQueryError struct{ Param string }

func (e *PageQueryError) Error() string { return "invalid scan page " + e.Param }

// PageQuery addresses one ordered window of scan summaries.
type PageQuery struct {
	Limit  int
	Cursor string
	Sort   string
	Order  string
}

type scanPageCursor struct {
	WatermarkOrdinal int64  `json:"watermark_ordinal"`
	Value            string `json:"value"`
	CreatedAt        string `json:"created_at"`
	ID               string `json:"id"`
}

type scanPageRow struct {
	id, value, createdAt, summary string
}

// ListPageByCanonicalPaths reads at most one page per root, without finding bodies.
func (s *SQLStore) ListPageByCanonicalPaths(ctx context.Context, paths []string, query PageQuery) (api.CodeScanPage, error) {
	paths = uniqueNonEmpty(paths)
	sort.Strings(paths)
	if query.Sort == "" {
		query.Sort = "date"
	}
	if query.Order == "" {
		query.Order = "desc"
	}
	expression, ok := map[string]string{"date": "created_at", "engine": "COALESCE(scanner_id, '')", "status": "status"}[query.Sort]
	if !ok {
		return api.CodeScanPage{}, &PageQueryError{Param: "sort"}
	}
	if query.Order != "asc" && query.Order != "desc" {
		return api.CodeScanPage{}, &PageQueryError{Param: "order"}
	}
	scope := pagecursor.Scope(append(paths, query.Sort, query.Order)...)
	cursor, err := decodeScanPageCursor(query.Cursor, scope)
	if err != nil {
		return api.CodeScanPage{}, err
	}
	limit := boundedScanPageLimit(query.Limit)
	if len(paths) == 0 {
		return api.CodeScanPage{Scans: []api.CodeScan{}}, nil
	}
	if cursor.WatermarkOrdinal == 0 {
		cursor.WatermarkOrdinal, err = s.queries.CodeScanPageWatermark(ctx)
		if err != nil {
			return api.CodeScanPage{}, err
		}
	}
	var rows []scanPageRow
	for _, path := range paths {
		page, err := s.scanRootPage(ctx, path, expression, query.Order, cursor, limit+1)
		if err != nil {
			return api.CodeScanPage{}, err
		}
		rows = append(rows, page...)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		comparison := strings.Compare(a.value, b.value)
		if comparison == 0 {
			comparison = strings.Compare(a.createdAt, b.createdAt)
		}
		if comparison == 0 {
			comparison = strings.Compare(a.id, b.id)
		}
		if query.Order == "asc" {
			return comparison < 0
		}
		return comparison > 0
	})
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	page := api.CodeScanPage{Scans: make([]api.CodeScan, 0, len(rows))}
	for _, row := range rows {
		var summary api.CodeScan
		if err := json.Unmarshal([]byte(row.summary), &summary); err != nil {
			return api.CodeScanPage{}, err
		}
		page.Scans = append(page.Scans, summary)
	}
	if hasMore {
		last := rows[len(rows)-1]
		cursor.Value, cursor.CreatedAt, cursor.ID = last.value, last.createdAt, last.id
		page.NextCursor, err = scanPages.Encode(scope, cursor)
	}
	return page, err
}

func (s *SQLStore) scanRootPage(ctx context.Context, path, expression, order string, cursor scanPageCursor, limit int) ([]scanPageRow, error) {
	direction, comparison := "DESC", "<"
	if order == "asc" {
		direction, comparison = "ASC", ">"
	}
	where := "canonical_path = ?"
	args := []any{path}
	orderFields := expression + " " + direction + ", created_at " + direction + ", id " + direction
	if expression == "created_at" {
		orderFields = "created_at " + direction + ", id " + direction
	}
	if cursor.ID != "" {
		if expression == "created_at" {
			where += " AND (created_at, id) " + comparison + " (?, ?)"
			args = append(args, cursor.CreatedAt, cursor.ID)
		} else {
			where += " AND (" + expression + ", created_at, id) " + comparison + " (?, ?, ?)"
			args = append(args, cursor.Value, cursor.CreatedAt, cursor.ID)
		}
	}
	args = append(args, cursor.WatermarkOrdinal, limit)
	// Expressions and directions are selected exclusively from the closed query vocabulary.
	query := `WITH selected AS MATERIALIZED (
  SELECT id, ` + expression + ` AS sort_value, created_at FROM code_scans
  WHERE ` + where + ` AND id IN (SELECT scan_id FROM code_scan_page_ordinals WHERE scan_id = code_scans.id AND ordinal <= ?)
  ORDER BY ` + orderFields + ` LIMIT ?
 ) SELECT selected.id, selected.sort_value, selected.created_at, list_json
 FROM selected CROSS JOIN scan_summaries ON scan_summaries.scan_id = selected.id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]scanPageRow, 0, limit)
	for rows.Next() {
		var row scanPageRow
		if err := rows.Scan(&row.id, &row.value, &row.createdAt, &row.summary); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func boundedScanPageLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	return min(limit, 100)
}

func decodeScanPageCursor(raw, scope string) (scanPageCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return scanPageCursor{}, nil
	}
	cursor, err := scanPages.Decode(raw, scope)
	if err != nil {
		return cursor, fmt.Errorf("%w: %w", ErrInvalidScanPageCursor, err)
	}
	if cursor.WatermarkOrdinal <= 0 || cursor.ID == "" || cursor.CreatedAt == "" {
		return cursor, fmt.Errorf("%w: %w", ErrInvalidScanPageCursor, pagecursor.ErrInvalid)
	}
	return cursor, nil
}
