package runeclamp

import (
	"testing"
	"unicode/utf8"
)

func TestClampKeepsShortText(t *testing.T) {
	t.Parallel()
	if got := Clamp("abc", 5); got != "abc" {
		t.Fatalf("Clamp = %q want unchanged", got)
	}
}

func TestClampMarksTheCut(t *testing.T) {
	t.Parallel()
	got := Clamp("abcdef", 3)
	if got != "abc"+Marker {
		t.Fatalf("Clamp = %q want abc%s", got, Marker)
	}
}

func TestClampCountsRunesNotBytes(t *testing.T) {
	t.Parallel()
	got := Clamp("héllo wörld", 5)
	if utf8.RuneCountInString(got) != 6 {
		t.Fatalf("Clamp = %q want 5 runes plus marker", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("Clamp = %q must not split a rune", got)
	}
}

func TestClampNonPositiveBudgetIsEmpty(t *testing.T) {
	t.Parallel()
	for _, max := range []int{0, -1, -99} {
		if got := Clamp("abc", max); got != "" {
			t.Fatalf("Clamp(_, %d) = %q want empty", max, got)
		}
	}
}

func TestFitKeepsMarkerInsideBudget(t *testing.T) {
	t.Parallel()
	for _, max := range []int{1, 2, 3, 8, 40} {
		got := Fit("abcdefghijklmnop", max)
		if n := utf8.RuneCountInString(got); n > max {
			t.Fatalf("Fit(_, %d) = %q is %d runes, over budget", max, got, n)
		}
	}
}

func TestFitNonPositiveBudgetIsEmpty(t *testing.T) {
	t.Parallel()
	if got := Fit("abc", 0); got != "" {
		t.Fatalf("Fit(_, 0) = %q want empty", got)
	}
}

func TestClampDoesNotLeaveTheMarkerFloating(t *testing.T) {
	t.Parallel()
	if got := Clamp("alpha beta", 6); got != "alpha…" {
		t.Fatalf("Clamp = %q want %q", got, "alpha…")
	}
	if got := Clamp("alpha\tbeta", 6); got != "alpha…" {
		t.Fatalf("Clamp over a tab = %q want %q", got, "alpha…")
	}
}

func TestFitDoesNotLeaveTheMarkerFloating(t *testing.T) {
	t.Parallel()
	if got := Fit("alpha beta", 7); got != "alpha…" {
		t.Fatalf("Fit = %q want %q", got, "alpha…")
	}
}

func TestClampKeepsInteriorWhitespace(t *testing.T) {
	t.Parallel()
	if got := Clamp("alpha beta gamma", 12); got != "alpha beta g…" {
		t.Fatalf("Clamp = %q want %q", got, "alpha beta g…")
	}
}
