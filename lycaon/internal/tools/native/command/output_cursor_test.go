package command

import (
	"encoding/json"
	"testing"
)

func TestParseCommandOutputCursorTreatsJSONNumbersAsDecimalText(t *testing.T) {
	for _, tc := range []struct {
		raw  any
		want int64
		ok   bool
	}{
		{nil, 0, true},
		{"42", 42, true},
		{json.Number("42"), 42, true},
		{json.Number("042"), 0, false},
		{"-1", 0, false},
		{json.Number("1e3"), 0, false},
		{int64(7), 7, true},
	} {
		got, err := parseCommandOutputCursor(tc.raw)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("parseCommandOutputCursor(%#v) = %d, %v; want %d ok=%t", tc.raw, got, err, tc.want, tc.ok)
		}
	}
}
