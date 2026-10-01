package sourceledger

import "sort"

// TextSpan identifies one visible run in normalized UTF-16 coordinates.
type TextSpan struct {
	Index  uint32 `json:"index"`
	Length uint32 `json:"length"`
	Client uint32 `json:"client"`
	Clock  uint32 `json:"clock"`
}

type TextState struct {
	DocumentID string
	Epoch      int64
	Revision   int64
	Spans      []TextSpan
}

// changedTextSpans finds character identities present only on one side.
func changedTextSpans(from, against []TextSpan) []TextSpan {
	byClient := make(map[uint32][]TextIdentityRange)
	for _, span := range against {
		byClient[span.Client] = append(byClient[span.Client], TextIdentityRange{Client: span.Client, Start: span.Clock, End: span.Clock + span.Length})
	}
	for _, ranges := range byClient {
		sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	}
	var changed []TextSpan
	for _, span := range from {
		cursor, end := span.Clock, span.Clock+span.Length
		ranges := byClient[span.Client]
		first := sort.Search(len(ranges), func(i int) bool { return ranges[i].End > cursor })
		for _, other := range ranges[first:] {
			if other.Start >= end {
				break
			}
			if cursor < other.Start {
				changed = append(changed, TextSpan{Index: span.Index + cursor - span.Clock, Length: other.Start - cursor, Client: span.Client, Clock: cursor})
			}
			cursor = max(cursor, min(other.End, end))
		}
		if cursor < end {
			changed = append(changed, TextSpan{Index: span.Index + cursor - span.Clock, Length: end - cursor, Client: span.Client, Clock: cursor})
		}
	}
	return changed
}

type textIdentityIndex map[uint32][]TextIdentityRange

func indexTextIdentities(spans []TextSpan) textIdentityIndex {
	index := textIdentityIndex{}
	for _, span := range spans {
		index[span.Client] = append(index[span.Client], TextIdentityRange{Client: span.Client, Start: span.Clock, End: span.Clock + span.Length})
	}
	for _, ranges := range index {
		sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	}
	return index
}

func (index textIdentityIndex) intersects(ranges []TextIdentityRange) bool {
	for _, interval := range ranges {
		spans := index[interval.Client]
		i := sort.Search(len(spans), func(i int) bool { return spans[i].End > interval.Start })
		if i < len(spans) && spans[i].Start < interval.End {
			return true
		}
	}
	return false
}
