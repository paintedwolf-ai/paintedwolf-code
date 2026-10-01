package promptattach

import (
	"fmt"
	"strings"
	"testing"
)

func TestSubjectBindingNoticeNamesMaterials(t *testing.T) {
	got := SubjectBindingNotice([]string{"CONTRIBUTING.md", " ", "CONTRIBUTING.md", "notes.txt"})
	if !strings.Contains(got, "CONTRIBUTING.md, notes.txt") {
		t.Fatalf("notice = %q", got)
	}
	if !strings.Contains(got, "Apply the user's request to those named materials first") {
		t.Fatalf("missing subject bind: %q", got)
	}
}

func TestSubjectBindingNoticeCapsLongLists(t *testing.T) {
	sources := make([]string, 0, maxSubjectNames+3)
	for i := 0; i < maxSubjectNames+3; i++ {
		sources = append(sources, fmt.Sprintf("f%d.txt", i))
	}
	got := SubjectBindingNotice(sources)
	if !strings.Contains(got, "+3 more") {
		t.Fatalf("expected overflow marker, got %q", got)
	}
}

func TestSubjectBindingNoticeEmptySources(t *testing.T) {
	got := SubjectBindingNotice(nil)
	if !strings.Contains(got, "user attachment(s)") || strings.Contains(got, ": .") {
		t.Fatalf("empty notice = %q", got)
	}
}
