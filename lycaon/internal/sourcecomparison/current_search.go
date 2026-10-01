package sourcecomparison

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/pkg/api"
)

// Find scans bounded windows with overlap for matches crossing display fragments.
func (d *CurrentDocument) Find(ctx context.Context, query string, cursor SearchCursor, limit int, sensitive bool) (SearchPage, error) {
	if query == "" || len(query) > 4096 || strings.ContainsAny(query, "\r\n") || !utf8.ValidString(query) || limit < 1 || limit > pagedview.MaxRows || cursor.Row < 0 || cursor.Row > d.Summary.Rows || cursor.Byte < 0 {
		return SearchPage{}, pagedview.ErrRange
	}
	pattern := regexp.QuoteMeta(query)
	if !sensitive {
		pattern = "(?i)" + pattern
	}
	matcher := regexp.MustCompile(pattern)
	page := SearchPage{Matches: []api.SourceReaderMatch{}, Next: cursor}
	for scanned := 0; page.Next.Row < d.Summary.Rows && scanned < 256<<10 && len(page.Matches) < limit; {
		if err := ctx.Err(); err != nil {
			return SearchPage{}, err
		}
		row, err := d.row(page.Next.Row)
		if err != nil {
			return SearchPage{}, err
		}
		from := page.Next.Byte
		if from > len(row.Text) || from < len(row.Text) && !utf8.RuneStart(row.Text[from]) {
			return SearchPage{}, pagedview.ErrRange
		}
		window := row.Text
		for next := page.Next.Row + 1; next < d.Summary.Rows && !strings.HasSuffix(window, "\n") && len(window)-len(row.Text) < 4*len(query); next++ {
			part, err := d.row(next)
			if err != nil {
				return SearchPage{}, err
			}
			if part.AfterLine != row.AfterLine {
				break
			}
			window += part.Text
		}
		scanned += len(row.Text)
		last := from
		for _, match := range matcher.FindAllStringIndex(window[from:], limit-len(page.Matches)) {
			start, end := from+match[0], from+match[1]
			if start >= len(row.Text) {
				break
			}
			page.Matches = append(page.Matches, api.SourceReaderMatch{Row: row.Index, From: width(window[:start]), To: width(window[:end])})
			last = end
		}
		if len(page.Matches) < limit {
			last = max(last, len(row.Text))
		}
		page.Next = SearchCursor{Row: row.Index, Byte: last}
		for page.Next.Byte >= len(row.Text) {
			page.Next.Byte -= len(row.Text)
			page.Next.Row++
			if page.Next.Row == d.Summary.Rows || page.Next.Byte == 0 {
				break
			}
			row, err = d.row(page.Next.Row)
			if err != nil {
				return SearchPage{}, err
			}
		}
	}
	if page.Next.Row == d.Summary.Rows && page.Next.Byte != 0 {
		return SearchPage{}, pagedview.ErrRange
	}
	page.Complete = page.Next.Row == d.Summary.Rows
	return page, nil
}
