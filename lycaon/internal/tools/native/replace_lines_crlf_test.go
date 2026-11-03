package native

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"testing"
)

func TestReplaceLineRange(t *testing.T) {
	for _, tc := range []struct {
		name, source            string
		start, end              int
		replacement, want, code string
	}{
		{"CRLF", "one\r\ntwo\r\nthree\r\n", 2, 2, "TWO\nEXTRA", "one\r\nTWO\r\nEXTRA\r\nthree\r\n", ""},
		{"LF", "one\ntwo\nthree\n", 2, 2, "TWO", "one\nTWO\nthree\n", ""},
		{"no final newline", "one\r\ntwo", 2, 2, "TWO", "one\r\nTWO", ""},
		{"empty", "", 1, 1, "first\n", "first\n", ""},
		{"remove", "one\ntwo\nthree\n", 2, 2, "", "one\nthree\n", ""},
		{"zero", "one\n", 0, 1, "", "", "REPLACE_LINES_INVALID_RANGE"},
		{"reversed", "one\ntwo\n", 2, 1, "", "", "REPLACE_LINES_INVALID_RANGE"},
		{"past EOF", "one\n", 2, 2, "", "", "REPLACE_LINES_BEYOND_EOF"},
		{"empty past EOF", "", 1, 2, "", "", "REPLACE_LINES_BEYOND_EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReplaceLineRange("a.txt", tc.source, tc.start, tc.end, tc.replacement)
			if tc.code != "" {
				reject := toolrejection.AsToolReject(err)
				if reject == nil || reject.Code != tc.code {
					t.Fatalf("error=%v want=%s", err, tc.code)
				}
				return
			}
			testutil.FailErr(t, "replace lines", err)
			if got != tc.want {
				t.Fatalf("got=%q want=%q", got, tc.want)
			}
		})
	}
}
func readFileString(t *testing.T, abs string) string {
	t.Helper()
	raw, err := os.ReadFile(abs) // #nosec G304 -- test-owned temp path.
	testutil.FailErr(t, "read back", err)
	return string(raw)
}
