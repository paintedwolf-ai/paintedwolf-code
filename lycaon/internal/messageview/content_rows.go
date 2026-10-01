package messageview

import (
	"sort"
	"unicode"

	"github.com/lycaon/lycaon/pkg/api"
)

// Oversized lines split at whitespace or a bounded rune count.
const ContentRowRunes = 2048

func ContentRowOffsets(text []rune) []int {
	offsets := []int{0}
	for start := 0; start < len(text); {
		end, space := min(start+ContentRowRunes, len(text)), -1
		for at := start; at < end; at++ {
			if text[at] == '\n' {
				end = at + 1
				space = -1
				break
			}
			if unicode.IsSpace(text[at]) {
				space = at + 1
			}
		}
		if end < len(text) && text[end-1] != '\n' && space > start {
			end = space
		}
		offsets = append(offsets, end)
		start = end
	}
	return offsets
}

func ContentRowAt(offsets []int, offset int) int {
	return max(0, sort.Search(len(offsets), func(i int) bool { return offsets[i] > offset })-1)
}

func ContentRows(text []rune, offsets []int, spans []api.RedactedSpan, start, limit, bytes int) []api.ChatContentRow {
	rows := make([]api.ChatContentRow, 0)
	for i := start; i < min(len(offsets)-1, start+limit); i++ {
		from, to := offsets[i], offsets[i+1]
		body := string(text[from:to])
		if len(rows) > 0 && len(body) > bytes {
			break
		}
		rows = append(rows, api.ChatContentRow{Index: i, Offset: from, Text: body, Spans: ContentRangeSpans(spans, from, to)})
		bytes -= len(body)
	}
	return rows
}

func ContentRangeSpans(spans []api.RedactedSpan, from, to int) []api.RedactedSpan {
	out := make([]api.RedactedSpan, 0)
	for _, span := range spans {
		start, end := max(from, span.Start), min(to, span.Start+span.Length)
		if end <= start {
			continue
		}
		span.Field, span.Start, span.Length = "content", start-from, end-start
		out = append(out, span)
	}
	return out
}

func prepareContentReference(field, callID, text string, spans []api.RedactedSpan) api.ChatContentReference {
	ref := ContentReference(field, callID, text)
	runes := []rune(text)
	offsets := ContentRowOffsets(runes)
	ref.Rows = len(offsets) - 1
	ref.PreviewRows = ContentRows(runes, offsets, spans, 0, 64, InlineContentBytes)
	return ref
}
