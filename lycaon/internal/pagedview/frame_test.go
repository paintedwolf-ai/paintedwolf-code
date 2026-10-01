package pagedview

import (
	"errors"
	"math"
	"testing"
)

func TestAnchorOffset(t *testing.T) {
	for _, test := range []struct {
		name                  string
		anchor, offset, total int64
		before                int
		follow                bool
		want                  int64
		invalid               bool
	}{
		{name: "relative", anchor: 100, offset: 250, total: 1000, want: 350},
		{name: "preceding context", anchor: 100, offset: 250, total: 1000, before: 20, want: 330},
		{name: "beginning", anchor: 3, total: 1000, before: 20, want: 0},
		{name: "latest tail", anchor: 100, offset: math.MaxInt64, total: 1000, before: 199, follow: true, want: 800},
		{name: "strict end", anchor: 100, offset: 900, total: 1000, want: 1000},
		{name: "strict range", anchor: 100, offset: 1000, total: 1000, invalid: true},
		{name: "empty", total: 0, follow: true, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := AnchorOffset(test.anchor, test.offset, test.total, test.before, test.follow)
			if test.invalid {
				if !errors.Is(err, ErrRange) {
					t.Fatalf("invalid range error=%v", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("offset=%d, error=%v; want %d", got, err, test.want)
			}
		})
	}
}
