package runematch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func starts(t *testing.T, p Pattern, text string, from, limit int) ([]int, int) {
	t.Helper()
	var out []int
	next, err := p.Scan(t.Context(), []rune(text), from, func(start int) bool {
		out = append(out, start)
		return len(out) < limit
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out, next
}

func TestScanFindsNonOverlappingMatchesInRunes(t *testing.T) {
	cases := []struct {
		name, query, text string
		sensitive         bool
		want              []int
	}{
		{"literal", "ab", "xxabxab", true, []int{2, 5}},
		{"non-overlapping", "aa", "aaaa", true, []int{0, 2}},
		{"repeated prefix", "aab", "aaab", true, []int{1}},
		{"insensitive", "ÄB", "xäbÄb", false, []int{1, 3}},
		{"sensitive", "ÄB", "xäbÄB", true, []int{3}},
		{"rune columns", "z", "日本z", true, []int{2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, next := starts(t, Compile(tc.query, tc.sensitive), tc.text, 0, 100)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("starts = %v, want %v", got, tc.want)
			}
			if next != len([]rune(tc.text)) {
				t.Fatalf("next = %d, want end %d", next, len([]rune(tc.text)))
			}
		})
	}
}

func TestScanStopsAndResumes(t *testing.T) {
	p := Compile("a", true)
	first, next := starts(t, p, "a-a-a", 0, 2)
	if !slices.Equal(first, []int{0, 2}) || next != 3 {
		t.Fatalf("first page = %v next %d", first, next)
	}
	rest, end := starts(t, p, "a-a-a", next, 10)
	if !slices.Equal(rest, []int{4}) || end != 5 {
		t.Fatalf("resumed page = %v next %d", rest, end)
	}
	if p.Len() != 1 {
		t.Fatalf("len = %d", p.Len())
	}
}

func TestScanHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Compile("x", true).Scan(ctx, []rune(strings.Repeat("y", 5000)), 0, func(int) bool { return true })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
}

func TestEmptyPatternMatchesNothing(t *testing.T) {
	got, next := starts(t, Compile("", true), "abc", 0, 10)
	if len(got) != 0 || next != 3 {
		t.Fatalf("empty pattern = %v next %d", got, next)
	}
}
