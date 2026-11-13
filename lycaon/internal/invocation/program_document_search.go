package invocation

import (
	"context"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/runematch"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// receiptSearchRows bounds the rows one search request renders and scans.
	receiptSearchRows  = 8192
	receiptSearchBatch = 256
)

type receiptSearchPosition struct {
	Row, Column int
}

var receiptSearchPages = pagecursor.For[receiptSearchPosition]("command_receipt_document_search")

// ReceiptSearch is one page of a search over a pinned receipt revision.
type ReceiptSearch struct {
	Revision, Query, Cursor string
	CaseSensitive           bool
	Limit                   int
}

// SearchReceiptDocument finds Query in the rows of one receipt revision. A page
// ends at Limit matches or after a bounded number of rows, so it may hold no
// matches and still continue.
func (r *SQLRecorder) SearchReceiptDocument(ctx context.Context, sessionID string, scopes []ReceiptScope, search ReceiptSearch) (api.CommandReceiptSearchPage, error) {
	out := api.CommandReceiptSearchPage{Matches: []api.CommandReceiptMatch{}}
	names := make([]string, len(scopes))
	for i, scope := range scopes {
		names[i] = scope.String()
	}
	cursorScope := pagecursor.Scope(sessionID, strings.Join(names, ","), search.Revision, search.Query, strconv.FormatBool(search.CaseSensitive))
	var at receiptSearchPosition
	if search.Cursor != "" {
		var err error
		if at, err = receiptSearchPages.Decode(search.Cursor, cursorScope); err != nil {
			return out, err
		}
	}
	tx, err := r.beginProgramRead(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	document, err := loadReceiptDocument(ctx, tx, sessionID, scopes)
	if err != nil {
		return out, err
	}
	if search.Revision != document.revision {
		return out, ErrReceiptChanged
	}
	if at.Row < 0 || at.Row > document.total || at.Column < 0 {
		return out, pagecursor.ErrInvalid
	}
	pattern := runematch.Compile(search.Query, search.CaseSensitive)
	stop := min(document.total, at.Row+receiptSearchRows)
	full := false
	for batch := at.Row; batch < stop && !full; batch += receiptSearchBatch {
		rows, err := document.read(ctx, tx, batch, min(stop, batch+receiptSearchBatch))
		if err != nil {
			return out, err
		}
		for _, row := range rows {
			column := 0
			if row.Index == at.Row {
				column = at.Column
			}
			text := []rune(row.Text)
			next, err := pattern.Scan(ctx, text, column, func(start int) bool {
				out.Matches = append(out.Matches, api.CommandReceiptMatch{Row: row.Index, Column: start, Length: pattern.Len()})
				return len(out.Matches) < search.Limit
			})
			if err != nil {
				return out, err
			}
			at = receiptSearchPosition{Row: row.Index + 1}
			if len(out.Matches) == search.Limit {
				if next < len(text) {
					at = receiptSearchPosition{Row: row.Index, Column: next}
				}
				full = true
				break
			}
		}
	}
	if !full {
		at = receiptSearchPosition{Row: stop}
	}
	if at.Row < document.total {
		if out.NextCursor, err = receiptSearchPages.Encode(cursorScope, at); err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}
