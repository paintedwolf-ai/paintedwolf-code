package sourcetree

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/pagedview"
)

type SearchMatch struct {
	Address  Address
	Index    int64
	From, To int
}
type SearchPage struct {
	Revision pagedview.Revision
	Matches  []SearchMatch
	Next     int64
	Complete bool
}

func (snapshot *snapshot) find(ctx context.Context, query string, offset int64, limit int, sensitive bool) (SearchPage, error) {
	pattern := regexp.QuoteMeta(query)
	if !sensitive {
		pattern = "(?i)" + pattern
	}
	matcher := regexp.MustCompile(pattern)
	result := SearchPage{Revision: snapshot.revision, Matches: []SearchMatch{}, Next: offset}
	base, scanned := int64(0), 0
	for _, root := range snapshot.roots {
		if result.Next >= base+root.rows {
			base += root.rows
			continue
		}
		for result.Next < base+root.rows && len(result.Matches) < limit && scanned < 1024 {
			rows, _, err := root.projection.Frame(ctx, result.Next-base, min(200, 1024-scanned))
			if err != nil {
				return SearchPage{}, err
			}
			for _, row := range rows {
				rank := result.Next
				result.Next++
				scanned++
				if row.Kind != "file" && row.Kind != "directory" {
					continue
				}
				if row.Address.Path == "." {
					row.Name = root.root.Label
				}
				match := matcher.FindStringIndex(row.Name)
				if match != nil {
					result.Matches = append(result.Matches, SearchMatch{Address: row.Address, Index: rank, From: utf16Width(row.Name[:match[0]]), To: utf16Width(row.Name[:match[1]])})
					if len(result.Matches) == limit {
						break
					}
				}
			}
			if len(rows) == 0 {
				return SearchPage{}, pagedview.ErrRange
			}
		}
		if len(result.Matches) == limit || scanned >= 1024 {
			break
		}
		base += root.rows
	}
	result.Complete = result.Next >= snapshot.total()
	return result, nil
}

func utf16Width(text string) int {
	units := 0
	for _, r := range text {
		units++
		if r > 0xffff {
			units++
		}
	}
	return units
}

func (p *Presentation) Find(ctx context.Context, basis, query string, offset int64, limit int, sensitive bool) (SearchPage, error) {
	if query == "" || len(query) > 4096 || !utf8.ValidString(query) || strings.ContainsAny(query, "\r\n") || offset < 0 || limit < 1 || limit > pagedview.MaxRows {
		return SearchPage{}, pagedview.ErrRange
	}

	if basis != "" && basis != p.revision.Projection {
		return SearchPage{}, pagedview.ErrRevision
	}
	frame, err := p.snapshot(ctx)
	if err != nil {
		return SearchPage{}, err
	}
	defer frame.Close()
	return frame.find(ctx, query, offset, limit, sensitive)
}
