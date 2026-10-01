package sourceledger

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestApplyAttributionInsertDeleteShift(t *testing.T) {
	before := "a\nb\nc\n"
	after := "a\nX\nc\n"
	got := ApplyAttribution(nil, before, after, "c1")
	if len(got) != 1 || got[0].Start != 2 || got[0].End != 2 || got[0].ChangeID != "c1" {
		t.Fatalf("insert middle: %+v", got)
	}

	existing := []Interval{{Start: 1, End: 3, ChangeID: "old"}}
	after2 := "a\nc\n"
	got = ApplyAttribution(existing, before, after2, "c2")
	// Surviving lines retain attribution after deletion.
	if len(got) != 1 || got[0].ChangeID != "old" || got[0].Start != 1 || got[0].End != 2 {
		t.Fatalf("delete middle: %+v", got)
	}
}

func TestNormalizeIntervalsNoOverlap(t *testing.T) {
	ivs := []Interval{
		{Start: 1, End: 3, ChangeID: "a"},
		{Start: 2, End: 5, ChangeID: "b"},
	}
	got := NormalizeIntervals(ivs, 10)
	for i := 1; i < len(got); i++ {
		if got[i].Start <= got[i-1].End {
			t.Fatalf("overlap: %+v", got)
		}
	}
}

func TestAttributionPropertyNoOverlap(t *testing.T) {
	cases := []struct{ before, after string }{
		{"", "x\n"},
		{"x\n", ""},
		{"a\nb\nc\n", "a\nb\nc\nd\n"},
		{"a\nb\nc\n", "a\nZ\nb\nc\n"},
		{"a\nb\nc\n", "c\nb\na\n"},
	}
	for _, tc := range cases {
		got := ApplyAttribution([]Interval{{Start: 1, End: 10, ChangeID: "seed"}}, tc.before, tc.after, "new")
		fileLen := len(splitLines(tc.after))
		for _, iv := range got {
			if iv.Start < 1 || iv.End > fileLen || iv.Start > iv.End {
				t.Fatalf("bounds: %+v fileLen=%d after=%q", iv, fileLen, tc.after)
			}
		}
		for i := 1; i < len(got); i++ {
			if got[i].Start <= got[i-1].End {
				t.Fatalf("overlap %+v", got)
			}
		}
	}
}

func TestAttributionCarriesFragmentedHistoryAcrossEdits(t *testing.T) {
	const lines = 3000
	before := make([]string, lines)
	existing := make([]Interval, lines)
	for i := range before {
		before[i] = fmt.Sprintf("line %d", i)
		existing[i] = Interval{Start: i + 1, End: i + 1, ChangeID: strconv.Itoa(i)}
	}
	after := append([]string{"inserted"}, before[1:lines-1]...)
	after = append(after, "replaced")
	got := ApplyAttribution(existing, strings.Join(before, "\n"), strings.Join(after, "\n"), "new")
	want := append([]Interval{{Start: 1, End: 1, ChangeID: "new"}}, existing[1:lines-1]...)
	want = append(want, Interval{Start: lines, End: lines, ChangeID: "new"})
	if !reflect.DeepEqual(got, want) {
		t.Fatal("surviving fragmented history changed")
	}
}

func TestAttributionKeepsUnknownGapsAfterLineShift(t *testing.T) {
	got := ApplyAttribution([]Interval{{Start: 2, End: 3, ChangeID: "old"}}, "a\nb\nc\nd\n", "X\na\nb\nc\nd\n", "new")
	want := []Interval{{Start: 1, End: 1, ChangeID: "new"}, {Start: 3, End: 4, ChangeID: "old"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attribution=%+v want=%+v", got, want)
	}
}

func FuzzAttributionOnlyCarriesMatchingLines(f *testing.F) {
	f.Add("a\nb\nc\n", "X\na\nc\n")
	f.Add("repeat\nrepeat\n", "repeat\nX\nrepeat\n")
	f.Add("", "new\n")
	f.Fuzz(func(t *testing.T, before, after string) {
		if len(before)+len(after) > 32_768 {
			return
		}
		oldLines, newLines := splitLines(before), splitLines(after)
		existing := make([]Interval, len(oldLines))
		for i := range oldLines {
			existing[i] = Interval{Start: i + 1, End: i + 1, ChangeID: strconv.Itoa(i)}
		}
		got := ApplyAttribution(existing, before, after, "new")
		previous := 0
		for _, iv := range got {
			if iv.Start <= previous || iv.End < iv.Start || iv.End > len(newLines) {
				t.Fatalf("invalid interval %+v", iv)
			}
			previous = iv.End
			if iv.ChangeID == "new" {
				continue
			}
			index, err := strconv.Atoi(iv.ChangeID)
			if err != nil || index < 0 || index >= len(oldLines) {
				t.Fatalf("invalid attribution identity %q", iv.ChangeID)
			}
			for line := iv.Start; line <= iv.End; line++ {
				if newLines[line-1] != oldLines[index] {
					t.Fatal("attribution transferred to a different line")
				}
			}
		}
	})
}
