package secretmatch

import "sort"

// ByteSpan identifies exact input bytes corresponding to a match's rune range.
type ByteSpan struct{ Start, End int }

// MatchByteSpans converts matches without decoding and re-encoding the input.
// Invalid UTF-8 bytes each occupy one rune offset, matching Go range semantics.
func MatchByteSpans(value string, hits []Match) []ByteSpan {
	bounds := make([]int, 0, 2*len(hits))
	for _, hit := range hits {
		bounds = append(bounds, hit.Start, hit.End)
	}
	offsets := byteOffsets(value, bounds)
	spans := make([]ByteSpan, 0, len(hits))
	for _, hit := range hits {
		start, okStart := offsets[hit.Start]
		end, okEnd := offsets[hit.End]
		if okStart && okEnd && end > start {
			spans = append(spans, ByteSpan{start, end})
		}
	}
	return spans
}

func byteOffsets(value string, bounds []int) map[int]int {
	sort.Ints(bounds)
	at := make(map[int]int, len(bounds))
	next, runeIndex := 0, 0
	for next < len(bounds) && bounds[next] < 0 {
		next++
	}
	for byteIndex := range value {
		for next < len(bounds) && bounds[next] == runeIndex {
			at[bounds[next]] = byteIndex
			next++
		}
		runeIndex++
	}
	for next < len(bounds) && bounds[next] == runeIndex {
		at[bounds[next]] = len(value)
		next++
	}
	return at
}
