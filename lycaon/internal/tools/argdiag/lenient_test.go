package argdiag

import (
	"slices"
	"strings"
	"testing"
)

func TestReadLenientJSONLocatesDefects(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		kind   DefectKind
		offset int
		open   []string
	}{
		{"unclosed root", `{"a": {"b": 1}`, DefectUnclosed, 14, []string{""}},
		{"unclosed nested", `{"a": [1, {"b": 2`, DefectUnclosed, 17, []string{"", "a", "a[1]"}},
		{"mismatched closer", `{"a": [{"b": 1}}`, DefectUnexpectedToken, 15, []string{"", "a"}},
		{"unterminated string", `{"a": "abc`, DefectUnterminatedString, 6, []string{""}},
		{"trailing text", `{"a": 1} x`, DefectTrailingText, 9, nil},
		{"bad literal", `{"a": tru}`, DefectUnexpectedToken, 6, []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := readLenientJSON(tc.text).Defect
			if got.Kind != tc.kind || got.Offset != tc.offset || !slices.Equal(got.OpenPaths, tc.open) {
				t.Fatalf("defect = %+v, want %s at %d open %v", got, tc.kind, tc.offset, tc.open)
			}
		})
	}
}

func TestReadLenientJSONKeepsStructureAsWritten(t *testing.T) {
	read := readLenientJSON(`{"claims": [], "coverage": {"revision": "r", "verdict": "X"}`)
	root, _ := read.Value.(map[string]any)
	coverage, _ := root["coverage"].(map[string]any)
	if coverage["verdict"] != "X" || root["verdict"] != nil {
		t.Fatalf("value = %#v, want verdict read inside coverage", read.Value)
	}
	if !slices.Equal(read.KeyOrder["coverage"], []string{"revision", "verdict"}) {
		t.Fatalf("coverage order = %v", read.KeyOrder["coverage"])
	}
}

func TestReadLenientJSONHandlesEscapes(t *testing.T) {
	read := readLenientJSON(`{"a": "quote \" and brace }", "b": [`)
	root, _ := read.Value.(map[string]any)
	if root["a"] != `quote " and brace }` || read.Defect.Kind != DefectUnclosed {
		t.Fatalf("value = %#v defect = %+v", read.Value, read.Defect)
	}
	if !strings.HasPrefix(read.Defect.OpenPaths[len(read.Defect.OpenPaths)-1], "b") {
		t.Fatalf("open paths = %v", read.Defect.OpenPaths)
	}
}

func TestReadLenientJSONRecoversMembersAfterAnEarlyClose(t *testing.T) {
	read := readLenientJSON(`{"goal": "g"}, "files": ["a"], "scope": {"mode": "write"}}`)
	if read.Defect.Kind != DefectTrailingText || read.Defect.Offset != 13 {
		t.Fatalf("defect = %+v, want trailing text at 13", read.Defect)
	}
	if !slices.Equal(read.TrailingOrder, []string{"files", "scope"}) || read.Trailing["scope"] == nil {
		t.Fatalf("trailing = %v %v", read.TrailingOrder, read.Trailing)
	}
}
