package summarize

import "testing"

func TestDedupeAnchors(t *testing.T) {
	anchors := []Anchor{
		{Path: "cmd/lycaon/main.go", Line: 29, Excerpt: "func run(args []string) error {"},
		{Path: "cmd/lycaon/main.go", Line: 29, Excerpt: "func run(args []string) error {"},
		{Path: "cmd/lycaon/main.go", Line: 76, Excerpt: "func runServe(dbPath string) error {"},
		{Path: "cmd/lycaon/main.go", Line: 76, Excerpt: "func runServe(dbPath string) error {"},
	}
	got := dedupeAnchors(anchors)
	if len(got) != 2 {
		t.Fatalf("anchors = %+v, want 2 unique path:line", got)
	}
}
