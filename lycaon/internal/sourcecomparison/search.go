package sourcecomparison

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/pkg/api"
)

// SearchCursor resumes in source coordinates, without recounting earlier matches.
type SearchCursor struct{ Row, Byte int }
type SearchPage struct {
	Matches  []api.SourceReaderMatch
	Next     SearchCursor
	Complete bool
}

// Find examines at most a bounded batch of logical lines. One source line is
// bounded by the document byte limit and may contain several display fragments.
func (d *Document) Find(ctx context.Context, query string, cursor SearchCursor, limit int, sensitive bool) (SearchPage, error) {
	if query == "" || len(query) > 4096 || strings.ContainsAny(query, "\r\n") || !utf8.ValidString(query) {
		return SearchPage{}, errors.New("find requires a nonempty UTF-8 line of at most 4096 bytes")
	}
	d.prepare()
	if limit < 1 || limit > pagedview.MaxRows || cursor.Row < 0 || cursor.Row > len(d.rows) || cursor.Byte < 0 {
		return SearchPage{}, pagedview.ErrRange
	}
	if cursor.Row == len(d.rows) {
		if cursor.Byte != 0 {
			return SearchPage{}, pagedview.ErrRange
		}
		return SearchPage{Matches: []api.SourceReaderMatch{}, Next: cursor, Complete: true}, nil
	}
	if d.rows[cursor.Row].Column != 0 {
		return SearchPage{}, pagedview.ErrRange
	}
	pattern := regexp.QuoteMeta(query)
	if !sensitive {
		pattern = "(?i)" + pattern
	}
	matcher := regexp.MustCompile(pattern)
	result := SearchPage{Matches: []api.SourceReaderMatch{}, Next: cursor}
	scanned := 0
	for result.Next.Row < len(d.rows) && len(result.Matches) < limit && scanned < 256<<10 {
		if err := ctx.Err(); err != nil {
			return SearchPage{}, err
		}
		start := result.Next.Row
		end := start + 1
		for end < len(d.rows) && d.rows[end].Column > 0 {
			end++
		}
		var text strings.Builder
		for at := start; at < end; at++ {
			text.WriteString(d.rows[at].Text)
		}
		line := text.String()
		from := result.Next.Byte
		if from > len(line) || from < len(line) && !utf8.RuneStart(line[from]) {
			return SearchPage{}, pagedview.ErrRange
		}
		scanned += len(line) - from
		matches := matcher.FindAllStringIndex(line[from:], limit-len(result.Matches))
		units, byteAt := width(line[:from]), from
		for _, match := range matches {
			first, last := from+match[0], from+match[1]
			units += width(line[byteAt:first])
			row := start
			for row+1 < end && d.rows[row+1].Column <= units {
				row++
			}
			matchWidth := width(line[first:last])
			result.Matches = append(result.Matches, api.SourceReaderMatch{Row: row, From: units - d.rows[row].Column, To: units + matchWidth - d.rows[row].Column})
			units, byteAt = units+matchWidth, last
		}
		if len(result.Matches) == limit && byteAt < len(line) {
			result.Next = SearchCursor{Row: start, Byte: byteAt}
		} else {
			result.Next = SearchCursor{Row: end}
		}
	}
	result.Complete = result.Next.Row == len(d.rows)
	return result, nil
}
