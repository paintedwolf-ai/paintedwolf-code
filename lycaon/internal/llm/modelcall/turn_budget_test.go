package modelcall

import (
	"testing"
)

func TestNoteCompletionTurnBudget(t *testing.T) {
	t.Cleanup(ResetTurnBudgetForTest)
	for _, tc := range []struct {
		name                string
		cap, total, visible int
		strict              bool
	}{
		{"empty at limit", 16384, 16384, 0, true},
		{"mostly reasoning", 32768, 27000, 1000, true},
		{"normal reasoning", 24576, 9000, 1000, false},
		{"visible output", 16384, 16384, 16384, false},
		{"unknown cap", 0, 16384, 0, false},
		{"unknown usage", 16384, 0, 0, false},
	} {
		NoteCompletionTurnBudget(tc.name, tc.cap, tc.total, tc.visible)
		if SessionStrictBudget(tc.name) != tc.strict {
			t.Errorf("%s: strict=%v", tc.name, SessionStrictBudget(tc.name))
		}
	}
}
