package documentcore

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReplacementEditsKeepChangedSpansTogether(t *testing.T) {
	for _, tc := range []struct {
		before, after string
		want          []Edit
	}{
		{"updated by a source action\n", "another disk update\n", []Edit{{Index: 0, Delete: 26, Insert: "another disk update"}}},
		{"🐺 x=1; y=1\n", "🐺 x=1; y=2\n", []Edit{{Index: 10, Delete: 1, Insert: "2"}}},
		{"a🐺z", "a🐶z", []Edit{{Index: 1, Delete: 2, Insert: "🐶"}}},
		{"a\nmiddle\nz\n", "A\nmiddle\nZ\n", []Edit{{Index: 9, Delete: 1, Insert: "Z"}, {Index: 0, Delete: 1, Insert: "A"}}},
		{"", "new", []Edit{{Insert: "new"}}},
		{"old", "", []Edit{{Delete: 3}}},
		{"same", "same", nil},
	} {
		edits, err := ReplacementEdits(tc.before, tc.after)
		testutil.FailErr(t, "plan replacement", err)
		if !reflect.DeepEqual(edits, tc.want) {
			t.Fatalf("replacement %q -> %q = %+v, want %+v", tc.before, tc.after, edits, tc.want)
		}
		actual, err := ApplyEdits(tc.before, edits)
		testutil.FailErr(t, "apply replacement", err)
		if actual != tc.after {
			t.Fatalf("replacement = %q, want %q", actual, tc.after)
		}
	}
}

func TestLineEditsPreserveCompleteRewrites(t *testing.T) {
	for _, pair := range [][2]string{
		{"", "new"}, {"old", ""}, {"a\n", "a\nb\n"},
		{"one\ntwo\nthree\n", "ONE\ntwo\nTHREE\n"},
		{"const fooBar = 1;\n", "const fetchBar = 1;\n"},
		{"🙂 first\nsecond", "🙂 new\nlast\n"},
	} {
		edits, err := LineEdits(pair[0], pair[1])
		testutil.FailErr(t, "plan line edits", err)
		actual, err := ApplyEdits(pair[0], edits)
		testutil.FailErr(t, "apply line edits", err)
		if actual != pair[1] {
			t.Fatalf("rewrite %q -> %q produced %q", pair[0], pair[1], actual)
		}
	}
}

func TestHunkEditsConfineEachHunkToItsOwnLines(t *testing.T) {
	guards, edits, err := HunkEdits("a\nb\n", "a\nc\nb\n")
	testutil.FailErr(t, "hunk edits for an inserted line", err)
	if len(guards) != 1 || guards[0] != (Edit{Index: 2, Delete: 0, Insert: "c\n"}) {
		t.Fatalf("guards = %+v, want one insertion at the line boundary", guards)
	}
	if len(edits) != 1 || edits[0] != (Edit{Index: 2, Delete: 0, Insert: "c\n"}) {
		t.Fatalf("edits = %+v, want the insertion at offset 2, not inside line 1", edits)
	}
	guards, edits, err = HunkEdits("one two\nthree\n", "one 2\nthree\nfour\n")
	testutil.FailErr(t, "hunk edits for a rewrite and an appended line", err)
	if len(guards) != 2 {
		t.Fatalf("guards = %+v, want the replaced line and the appended line", guards)
	}
	for _, edit := range edits {
		inside := false
		for _, guard := range guards {
			if edit.Index >= guard.Index && edit.Index+edit.Delete <= guard.Index+guard.Delete {
				inside = true
			}
		}
		if !inside {
			t.Fatalf("edit %+v escapes its guarded hunk %+v", edit, guards)
		}
	}
}
