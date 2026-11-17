package survey

import (
	"testing"
)

// A read's end line is the last line it shows, whether or not the file ends in a newline.
func TestReadEndsAtTheLastLineShown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text string
		want int
	}{{"a\nb\n", 2}, {"a\nb", 2}, {"a\n\n", 2}} {
		page, total, end, more := PaginateLines(tc.text, 1, 100)
		if end != tc.want || total != tc.want || len(page) != tc.want || more {
			t.Fatalf("%q: page=%d total=%d end=%d more=%t, want %d", tc.text, len(page), total, end, more, tc.want)
		}
	}
}
