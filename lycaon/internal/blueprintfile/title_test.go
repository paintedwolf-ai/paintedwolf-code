package blueprintfile

import "testing"

func TestIsPlaceholderTitle(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"", true},
		{"blueprint", true},
		{"Blueprint", true},
		{" plan ", true},
		{"PLAN", true},
		{"Ship it", false},
		{"Blueprint 2", false},
		{"plan review", false},
	}
	for _, tc := range cases {
		if got := IsPlaceholderTitle(tc.title); got != tc.want {
			t.Errorf("IsPlaceholderTitle(%q) = %v want %v", tc.title, got, tc.want)
		}
	}
}

func TestDeclaredTitle(t *testing.T) {
	if title, ok := DeclaredTitle("---\ntitle: Ship it\n---\n"); !ok || title != "Ship it" {
		t.Fatalf("declared = %q ok=%v", title, ok)
	}
	if _, ok := DeclaredTitle("---\ntitle: blueprint\n---\n"); ok {
		t.Fatal("placeholder title must not count as declared")
	}
	if _, ok := DeclaredTitle("---\nresearch_depth: none\n---\n"); ok {
		t.Fatal("missing title must not count as declared")
	}
	if _, ok := DeclaredTitle("## Goal\n"); ok {
		t.Fatal("body-only must not count as declared")
	}
}
