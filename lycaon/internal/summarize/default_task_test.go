package summarize

import "testing"

func TestDefaultTask(t *testing.T) {
	if got := DefaultTask("focus", "a.go", nil, ""); got != "focus" {
		t.Fatalf("explicit task = %q", got)
	}
	if got := DefaultTask("", "pkg/a.go", nil, ""); got != "Overview of pkg/a.go" {
		t.Fatalf("path default = %q", got)
	}
	if got := DefaultTask("", "", []string{"a.go", "b.go"}, ""); got != "Overview of a.go and related paths" {
		t.Fatalf("paths default = %q", got)
	}
	if got := DefaultTask("", "", nil, "code"); got != "Overview of the attached material" {
		t.Fatalf("content default = %q", got)
	}
}
