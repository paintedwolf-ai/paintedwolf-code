//go:build unix

package toolhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandEntrySedTextForms(t *testing.T) {
	f := newPathsFixture(t)
	path := filepath.Join(f.Dir, "lines.txt")
	testutil.FailErr(t, "seed sed input", os.WriteFile(path, []byte("alpha\ngamma\n"), 0o600))
	out, err := f.Call(t, t.Context(), "sed -i '' -e 's/alpha/ALPHA/g' -e '/gamma/d' -e '1a\\\nend' lines.txt; cat lines.txt")
	testutil.FailErr(t, "run native BSD sed form", err)
	got := decodeProgramOutcome(t, out)
	if got.Status != 0 || got.Stdout != "ALPHA\nend\n" {
		t.Fatalf("sed outcome: %+v", got)
	}
	out, err = f.Call(t, t.Context(), "printf 'alpha\\ngamma\\n' | sed -e '/gamma/d' -e '$a\\\nend'")
	testutil.FailErr(t, "run append after deleting addressed last line", err)
	got = decodeProgramOutcome(t, out)
	if got.Status != 0 || got.Stdout != "alpha\n" {
		t.Fatalf("sed deleted-line outcome: %+v", got)
	}
	for _, tc := range []struct{ script, want string }{
		{"1a next", "alpha\nnext\n"},
		{"1i before", "before\nalpha\n"},
		{"1c changed", "changed\n"},
	} {
		out, err := f.Call(t, t.Context(), "printf 'alpha\\n' | sed -e '"+tc.script+"'")
		testutil.FailErr(t, "run native GNU sed form", err)
		got := decodeProgramOutcome(t, out)
		if got.Status != 0 || got.Stdout != tc.want {
			t.Fatalf("sed %q outcome: %+v", tc.script, got)
		}
	}
}

func TestCommandEntryBinarySearchContinuesWithoutMatchBudget(t *testing.T) {
	f := newPathsFixture(t)
	data := append([]byte("TODO\x00"), make([]byte, 300<<10)...)
	testutil.FailErr(t, "seed binary input", os.WriteFile(filepath.Join(f.Dir, "binary.dat"), data, 0o600))
	for _, tc := range []struct {
		program, notice string
		status          int
	}{
		{"grep -rn TODO .", "binary.dat: binary file matches", 0},
		{"rg -n TODO", "", 1},
		{"rg -n TODO binary.dat", "binary file matches (found \"\\0\" byte around offset 4)", 0},
		{"rg -uuu -n TODO", "binary.dat: binary file matches (found \"\\0\" byte around offset 4)", 0},
	} {
		out, err := f.Call(t, t.Context(), tc.program)
		testutil.FailErr(t, "run native binary search", err)
		got := decodeProgramOutcome(t, out)
		if got.Status != tc.status || (tc.notice != "" && !strings.Contains(got.Stdout+got.Stderr, tc.notice)) || strings.Contains(got.Stderr, "GREP_MATCH_BUDGET") {
			t.Fatalf("%s: %+v", tc.program, got)
		}
	}
}
