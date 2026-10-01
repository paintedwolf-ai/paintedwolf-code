package sourceloc

import "testing"

func TestCutReadsTrailingLocations(t *testing.T) {
	for _, tc := range []struct {
		in, path string
		want     Location
		ok       bool
	}{
		{"a.go:12", "a.go", Location{Line: 12}, true},
		{"a.go:12:3", "a.go", Location{Line: 12, Column: 3}, true},
		{"a.go:10-20", "a.go", Location{Line: 10, EndLine: 20}, true},
		{"a.go:20-10", "a.go", Location{Line: 20}, true},
		{"a.go#L40C2-L52", "a.go", Location{Line: 40, Column: 2, EndLine: 52}, true},
		{"a.go#L40-52", "a.go", Location{Line: 40, EndLine: 52}, true},
		{"a.go#lines-40:52", "a.go", Location{Line: 40, EndLine: 52}, true},
		{"a.cs(12,3)", "a.cs", Location{Line: 12, Column: 3}, true},
		{"a.go:12:0", "a.go", Location{Line: 12}, true},
		{"a.go:0", "a.go:0", Location{}, false},
		{":12", ":12", Location{}, false},
		{"report (1)", "report (1)", Location{}, false},
		{"foo:bar.ts:9", "foo:bar.ts", Location{Line: 9}, true},
	} {
		path, loc, ok := Cut(tc.in)
		if path != tc.path || loc != tc.want || ok != tc.ok {
			t.Errorf("Cut(%q) = %q %+v %t, want %q %+v %t", tc.in, path, loc, ok, tc.path, tc.want, tc.ok)
		}
	}
}
