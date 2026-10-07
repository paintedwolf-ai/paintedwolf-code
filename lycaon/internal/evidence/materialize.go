package evidence

import (
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

// Span is a line-bounded excerpt within one path, materialized from ledger records.
type Span struct {
	Path       string
	LineRanges []LineRange
}

// Materialize returns verbatim body lines for span from the ledger snapshot.
func Materialize(ledger Ledger, span Span) []string {
	path := strings.TrimSpace(span.Path)
	if path == "" || ledger.ByPath == nil {
		return nil
	}
	handles := ledger.ByPath[path]
	if len(handles) == 0 {
		return nil
	}
	var out []string
	for i := len(handles) - 1; i >= 0; i-- {
		rec, ok := ledger.Handles[handles[i]]
		if !ok {
			continue
		}
		lines := materializeFromRecord(rec, span.LineRanges)
		if len(lines) > 0 {
			out = append(out, lines...)
		}
	}
	return out
}

func materializeFromRecord(rec Record, want []LineRange) []string {
	if len(rec.Body) == 0 {
		return nil
	}
	if len(want) == 0 {
		return append([]string(nil), rec.Body...)
	}
	var out []string
	for _, chunk := range rec.Body {
		for _, line := range strings.Split(chunk, "\n") {
			n := lineNumberFromBodyLine(line)
			if n <= 0 {
				continue
			}
			for _, r := range want {
				if n >= r.Start && n <= r.End {
					out = append(out, line)
					break
				}
			}
		}
	}
	return out
}

func lineNumberFromBodyLine(line string) int {
	n, _, ok := hostmarker.ParseNumberedLine(line)
	if !ok || n <= 0 {
		return 0
	}
	return n
}
