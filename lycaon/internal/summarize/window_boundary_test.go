package summarize

import (
	"strings"
	"testing"
)

// The label's last line is the last line shown. A live window for a 31-line file said
// 13-32 and held a blank row, so counting back from the end put every line one too far.
func TestWindowEndsAtTheLastLineShown(t *testing.T) {
	t.Parallel()
	const head = "package a\n\nfunc main() {\n\trun()\n}\n"
	for _, tc := range []struct {
		name  string
		head  string
		lines []string
		want  int
	}{
		{name: "split by the window", head: head, want: 5},
		{name: "split by the caller", head: head, lines: strings.Split(head, "\n"), want: 5},
		{name: "no final newline", head: strings.TrimSuffix(head, "\n"), want: 5},
		{name: "a real blank last line stays", head: head + "\n", want: 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w, ok := symbolWindowLines(DefinitionItem{RelPath: "a.go", Name: "main", Head: tc.head, StartLine: 1, Line: 3}, 50, tc.lines)
			if !ok || w.StartLine != 3 || w.EndLine != tc.want {
				t.Fatalf("window = %+v ok=%t, want 3-%d", w, ok, tc.want)
			}
			rows := strings.Split(w.Body, "\n")
			if len(rows) != w.EndLine-w.StartLine+1 {
				t.Fatalf("%d rows for lines %d-%d: %q", len(rows), w.StartLine, w.EndLine, w.Body)
			}
		})
	}
}
