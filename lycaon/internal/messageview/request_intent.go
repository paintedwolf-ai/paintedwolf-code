package messageview

import (
	"slices"

	"github.com/lycaon/lycaon/pkg/api"
)

// PreserveRequestIntent preserves the original instruction through view replacement.
func PreserveRequestIntent(rows, source []api.Message) []api.Message {
	intent, ok := api.LastUserIntentMessage(source)
	if !ok || intent.ID == "" {
		return rows
	}
	intent.ContextPinned = true
	for i, row := range rows {
		if row.ID == intent.ID {
			out := slices.Clone(rows)
			out[i] = intent
			return out
		}
	}
	at := 0
	for at < len(rows) && rows[at].Ord <= 0 {
		at++
	}
	return slices.Insert(slices.Clone(rows), at, intent)
}
