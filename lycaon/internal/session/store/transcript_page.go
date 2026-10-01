package store

import (
	"github.com/lycaon/lycaon/pkg/api"
)

// pageAscendingWindow selects an ascending ordinal window.
func pageAscendingWindow(all []api.Message, q api.TranscriptPageQuery) []api.Message {
	limit := q.EffectiveLimit()
	if len(all) == 0 || limit <= 0 {
		return nil
	}
	switch {
	case q.Before != nil:
		bound := *q.Before
		end := 0
		for end < len(all) && all[end].Ord < bound {
			end++
		}
		start := end - limit
		if start < 0 {
			start = 0
		}
		if start >= end {
			return nil
		}
		out := make([]api.Message, end-start)
		copy(out, all[start:end])
		return out
	case q.After != nil:
		bound := *q.After
		start := 0
		for start < len(all) && all[start].Ord <= bound {
			start++
		}
		end := start + limit
		if end > len(all) {
			end = len(all)
		}
		if start >= end {
			return nil
		}
		out := make([]api.Message, end-start)
		copy(out, all[start:end])
		return out
	default:
		// Tail: newest `limit` rows.
		start := len(all) - limit
		if start < 0 {
			start = 0
		}
		out := make([]api.Message, len(all)-start)
		copy(out, all[start:])
		return out
	}
}

func reverseMessages(msgs []api.Message) {
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
}

func windowHasMore(all []api.Message, window []api.Message) (before, after bool) {
	if len(window) == 0 {
		return len(all) > 0, false
	}
	oldest, newest := window[0].Ord, window[len(window)-1].Ord
	if len(all) == 0 {
		return false, false
	}
	return all[0].Ord < oldest, all[len(all)-1].Ord > newest
}
