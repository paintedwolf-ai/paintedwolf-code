package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSlugProjectName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"lycaon", "lycaon"},
		{"Deal finder", "deal-finder"},
		{"  Foo--Bar!!  ", "foo-bar"},
		{"", "root"},
	}
	for _, tc := range tests {
		if got := SlugProjectName(tc.in); got != tc.want {
			t.Fatalf("SlugProjectName(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestDerivedRootLabelPreservesFolderNameAndBoundsCollisionSuffix(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "Painted Wolf")
	b := filepath.Join(dir, "other", "Painted Wolf")
	for _, p := range []string{a, b} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
	}
	first := deriveUniqueRootLabel(nil, a)
	second := deriveUniqueRootLabel([]string{strings.ToLower(first)}, b)
	if first != "Painted Wolf" {
		t.Fatalf("first = %q want Painted Wolf", first)
	}
	if second != "Painted Wolf-2" {
		t.Fatalf("second = %q want Painted Wolf-2", second)
	}

	long := strings.Repeat("界", MaxRootDisplayLabelRunes)
	bounded := deriveUniqueRootLabel([]string{long}, filepath.Join(dir, long))
	if utf8.RuneCountInString(bounded) != MaxRootDisplayLabelRunes {
		t.Fatalf("bounded label length = %d want %d", utf8.RuneCountInString(bounded), MaxRootDisplayLabelRunes)
	}
	if !strings.HasSuffix(bounded, "-2") {
		t.Fatalf("bounded label = %q want collision suffix", bounded)
	}
}

func TestDerivedRootLabelSanitizesAddressingDelimiters(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "@backend", want: "backend"},
		{name: "line\nbreak", want: "line-break"},
		{name: "tab\tbreak", want: "tab-break"},
		{name: "back\\slash", want: "back-slash"},
	} {
		if got := deriveUniqueRootLabel(nil, filepath.Join(dir, tc.name)); got != tc.want {
			t.Fatalf("deriveUniqueRootLabel(%q) = %q want %q", tc.name, got, tc.want)
		}
	}
}
