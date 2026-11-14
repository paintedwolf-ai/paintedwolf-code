package projectroot

import "testing"

func baselineParseRoots() []RootRef {
	return []RootRef{
		{ID: "primary-id", Label: "app", Path: "/project/app", IsPrimary: true},
		{ID: "secondary-id", Label: "docs", Path: "/project/docs"},
		{ID: "tertiary-id", Path: "/project/misc"},
	}
}

func TestParseBaselinePathBareIsPrimaryRelative(t *testing.T) {
	roots := baselineParseRoots()
	root, rel, ok := ParseBaselinePath(roots, "notes/draft.md")
	if !ok {
		t.Fatalf("ParseBaselinePath(%q) not ok", "notes/draft.md")
	}
	if root.ID != "primary-id" || rel != "notes/draft.md" {
		t.Fatalf("got root=%q rel=%q want primary-id notes/draft.md", root.ID, rel)
	}
}

func TestParseBaselinePathLabelCaseInsensitive(t *testing.T) {
	roots := baselineParseRoots()
	root, rel, ok := ParseBaselinePath(roots, "@DOCS/Plan.md")
	if !ok {
		t.Fatalf("ParseBaselinePath label not ok")
	}
	if root.ID != "secondary-id" || rel != "Plan.md" {
		t.Fatalf("got root=%q rel=%q want secondary-id Plan.md", root.ID, rel)
	}
}

func TestParseBaselinePathBranchRelativeCompat(t *testing.T) {
	roots := baselineParseRoots()
	dir, err := BranchDirForID("secondary-id")
	if err != nil {
		t.Fatalf("BranchDirForID: %v", err)
	}
	root, rel, ok := ParseBaselinePath(roots, dir+"/plan.md")
	if !ok {
		t.Fatalf("ParseBaselinePath branch-relative not ok")
	}
	if root.ID != "secondary-id" || rel != "plan.md" {
		t.Fatalf("got root=%q rel=%q want secondary-id plan.md", root.ID, rel)
	}
}

func TestParseBaselinePathRejectsUnsafeAndUnknown(t *testing.T) {
	roots := baselineParseRoots()
	for _, raw := range []string{"", "   ", "../escape.md", "a/../../escape.md", "/abs/path.md", "@nope/x.md", "@", "app/../../x.md"} {
		if root, rel, ok := ParseBaselinePath(roots, raw); ok {
			t.Fatalf("ParseBaselinePath(%q) = (%q, %q, true) want rejected", raw, root.ID, rel)
		}
	}
}

func TestParseBaselinePathLabelOnlyAddressesRoot(t *testing.T) {
	roots := baselineParseRoots()
	root, rel, ok := ParseBaselinePath(roots, "@docs")
	if !ok {
		t.Fatalf("ParseBaselinePath(@docs) not ok")
	}
	if root.ID != "secondary-id" || rel != "." {
		t.Fatalf("got root=%q rel=%q want secondary-id .", root.ID, rel)
	}
}

func TestBaselineDisplayPathKeysOnPrimaryRoot(t *testing.T) {
	roots := baselineParseRoots()
	cases := []struct {
		rootID, rel, want string
	}{
		{"primary-id", "notes.md", "notes.md"},
		{"secondary-id", "plan.md", "@docs/plan.md"},
		{"tertiary-id", "misc.md", "misc.md"},
	}
	for _, tc := range cases {
		if got := BaselineDisplayPath(roots, tc.rootID, tc.rel); got != tc.want {
			t.Fatalf("BaselineDisplayPath(%q, %q) = %q want %q", tc.rootID, tc.rel, got, tc.want)
		}
	}
}

func TestBaselineDisplayPathRoundTripsParseBaselinePath(t *testing.T) {
	roots := baselineParseRoots()
	for _, raw := range []string{"notes.md", "@docs/plan.md", "misc.md", "a/b/c.md"} {
		root, rel, ok := ParseBaselinePath(roots, raw)
		if !ok {
			t.Fatalf("ParseBaselinePath(%q) not ok", raw)
		}
		if got := BaselineDisplayPath(roots, root.ID, rel); got != raw {
			t.Fatalf("round trip %q -> (%q, %q) -> %q", raw, root.ID, rel, got)
		}
	}
}
