package textsearch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// render prints scanned lines as grep -n does: N:TEXT, N-TEXT and "--".
func render(t *testing.T, content string, sel Selector, opts ScanOptions) (string, int) {
	t.Helper()
	var out strings.Builder
	n, err := ScanLines(t.Context(), []byte(content), sel, opts, func(l Line) error {
		if l.Gap {
			out.WriteString("--\n")
		}
		sep := ":"
		if l.Kind == ContextLine {
			sep = "-"
		}
		fmt.Fprintf(&out, "%d%s%s\n", l.Number, sep, content[l.Start:l.End])
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out.String(), n
}

func TestScanLinesContextAndLimits(t *testing.T) {
	m, err := Compile(t.Context(), Spec{Patterns: []string{"m"}, Dialect: Basic}, catalogLimits(t))
	testutil.FailErr(t, "compile text-search pattern", err)
	const text = "1\n2\nm\n3\n4\n5\n6\nm\n7\n"
	for _, c := range []struct {
		name    string
		content string
		opts    ScanOptions
		want    string
		count   int
	}{
		{"plain", text, ScanOptions{MaxSelected: -1}, "3:m\n8:m\n", 2},
		{"context groups", text, ScanOptions{MaxSelected: -1, Before: 1, After: 1}, "2-2\n3:m\n4-3\n--\n7-6\n8:m\n9-7\n", 2},
		{"adjacent groups merge", text, ScanOptions{MaxSelected: -1, Before: 2, After: 2}, "1-1\n2-2\n3:m\n4-3\n5-4\n6-5\n7-6\n8:m\n9-7\n", 2},
		{"invert", "a\nm\nb\n", ScanOptions{MaxSelected: -1, Invert: true}, "1:a\n3:b\n", 2},
		{"invert with context", "a\nm\nb\n", ScanOptions{MaxSelected: -1, Invert: true, After: 1}, "1:a\n2-m\n3:b\n", 2},
		{"max count keeps trailing context", "m\nm\nb\n", ScanOptions{MaxSelected: 1, After: 1}, "1:m\n2-m\n", 1},
		{"max count zero reads nothing", text, ScanOptions{MaxSelected: 0}, "", 0},
		{"final line without LF", "x\nm", ScanOptions{MaxSelected: -1}, "2:m\n", 1},
		{"empty records", "\n\nm\n", ScanOptions{MaxSelected: -1, Before: 5}, "1-\n2-\n3:m\n", 1},
		{"no input", "", ScanOptions{MaxSelected: -1}, "", 0},
		{"carriage return kept", "m\r\n", ScanOptions{MaxSelected: -1}, "1:m\r\n", 1},
		{"carriage return trimmed", "m\r\n", ScanOptions{MaxSelected: -1, TrimCR: true}, "1:m\n", 1},
		{"null data", "a\x00m\nx\x00m", ScanOptions{MaxSelected: -1, NullData: true}, "2:m\nx\n3:m\n", 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, n := render(t, c.content, m, c.opts)
			if got != c.want || n != c.count {
				t.Fatalf("got %q (%d), want %q (%d)", got, n, c.want, c.count)
			}
		})
	}
}

type failingSelector struct{ err error }

func (f failingSelector) Select(context.Context, []byte) (Span, bool, error) {
	return Span{}, false, f.err
}

func TestScanLinesStopsOnRecordErrorsAndStopScan(t *testing.T) {
	budget := &BudgetError{Resource: "regex_work_steps", Maximum: 1, Observed: 2}
	if _, err := ScanLines(t.Context(), []byte("a\n"), failingSelector{budget}, ScanOptions{MaxSelected: -1}, func(Line) error { return nil }); !errors.Is(err, budget) {
		t.Fatalf("record error: %v", err)
	}
	m, err := Compile(t.Context(), Spec{Patterns: []string{"a"}, Dialect: Fixed}, catalogLimits(t))
	testutil.FailErr(t, "compile text-search pattern", err)
	seen := 0
	n, err := ScanLines(t.Context(), []byte("a\na\na\n"), m, ScanOptions{MaxSelected: -1}, func(Line) error {
		seen++
		return StopScan
	})
	if err != nil || n != 1 || seen != 1 {
		t.Fatalf("stop: n=%d seen=%d err=%v", n, seen, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ScanLines(ctx, []byte("a\n"), m, ScanOptions{MaxSelected: -1}, func(Line) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan: %v", err)
	}
}
