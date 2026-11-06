package evidence

import (
	"reflect"
	"testing"
)

func TestRangeObserved(t *testing.T) {
	t.Parallel()
	interval := Record{Path: "a.go", LineRanges: []LineRange{{Start: 20, End: 22}, {Start: 10, End: 12}, {Start: 13, End: 15}, {Start: 14, End: 14}}}
	numbered := Record{Path: "a.go", Body: []string{"5|five\n6|six\n8|eight"}}
	grep := Record{grepLines: map[string]map[int]string{"a.go": {3: "c", 4: "d", 7: "g"}}, pathsTouched: []string{"a.go", "b.go"}}
	cases := []struct {
		name       string
		rec        Record
		path       string
		start, end int
		want       bool
	}{
		{"single interval", interval, "a.go", 20, 22, true},
		{"abutting intervals out of order", interval, "a.go", 10, 15, true},
		{"gap between intervals", interval, "a.go", 15, 20, false},
		{"past the end", interval, "a.go", 21, 23, false},
		{"before the start", interval, "a.go", 9, 10, false},
		{"record path by default", interval, "", 10, 12, true},
		{"another path", interval, "b.go", 10, 12, false},
		{"zero start", interval, "a.go", 0, 2, false},
		{"backward", interval, "a.go", 12, 10, false},
		{"numbered body", numbered, "a.go", 5, 6, true},
		{"numbered body gap", numbered, "a.go", 6, 8, false},
		{"longer than the body", numbered, "a.go", 1, 100, false},
		{"grep lines", grep, "a.go", 3, 4, true},
		{"grep gap", grep, "a.go", 4, 7, false},
		{"grep path with no lines", grep, "b.go", 3, 4, false},
		{"no line facts", Record{Path: "a.go"}, "a.go", 1, 2, false},
		{"pathless record", Record{}, "", 1, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := RangeObserved(tc.rec, tc.path, tc.start, tc.end); got != tc.want {
				t.Fatalf("RangeObserved = %v", got)
			}
		})
	}
}

func TestExcerptAnchors(t *testing.T) {
	t.Parallel()
	rec := Record{Path: "a.go", Body: []string{"1|needle here now\n2|other\n3|needle here now\n4|tail line text"}}
	if got := ExcerptAnchors(rec, "a.go", "needle here"); !reflect.DeepEqual(got, []int{1, 3}) {
		t.Fatalf("anchors = %v", got)
	}
	if got := ExcerptAnchors(rec, "a.go", "needle here now\ntail line text"); !reflect.DeepEqual(got, []int{3}) {
		t.Fatalf("multi-line anchors = %v", got)
	}
	if got := ExcerptAnchors(rec, "a.go", "needle"); got != nil {
		t.Fatalf("short excerpt anchors = %v", got)
	}
	if got := ExcerptAnchors(rec, "a.go", "        "); got != nil {
		t.Fatalf("blank excerpt anchors = %v", got)
	}
}
