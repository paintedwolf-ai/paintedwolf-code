package command_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgramEntryEvalUsesCurrentShellAndAdmission(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), `value=alpha; eval 'printf "%s\\n" "$value"'`)
	testutil.FailErr(t, "run eval in current shell", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || got.Stdout != "alpha\n" {
		t.Fatalf("eval outcome: %+v", got)
	}
}

func TestProgramEntryPsReportsObservedColumns(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), fmt.Sprintf("ps -p %d -o pcpu,pmem,vsz,rss,tty,stat,start,time", os.Getpid()))
	testutil.FailErr(t, "inspect process columns", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 {
		t.Fatalf("ps outcome: %+v", got)
	}
	lines := strings.Split(strings.TrimSpace(got.Stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("ps rows: %q", got.Stdout)
	}
	columns := strings.Fields(lines[1])
	if len(columns) != 8 {
		t.Fatalf("ps columns: %q", lines[1])
	}
	for i, column := range columns {
		if column == "-" {
			t.Fatalf("ps column %d missing an observable value: %q", i, lines[1])
		}
	}
}

func TestProgramEntrySedTextCommandsAndBSDInPlace(t *testing.T) {
	f := newProgramFixture(t)
	path := filepath.Join(f.Dir, "lines.txt")
	testutil.FailErr(t, "seed sed input", os.WriteFile(path, []byte("alpha\ngamma\n"), 0o600))
	out, err := f.call(t, t.Context(), "sed -i '' -e 's/alpha/ALPHA/g' -e '/gamma/d' -e '$a\\\nend' lines.txt; cat lines.txt")
	testutil.FailErr(t, "run BSD in-place sed with text command", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || got.Stdout != "ALPHA\nend\n" {
		t.Fatalf("sed outcome: %+v", got)
	}
}

func TestProgramEntrySedGNUTextForms(t *testing.T) {
	for _, tc := range []struct{ script, want string }{
		{"1a next", "alpha\nnext\n"},
		{"1i before", "before\nalpha\n"},
		{"1c changed", "changed\n"},
	} {
		f := newProgramFixture(t)
		out, err := f.call(t, t.Context(), "printf 'alpha\\n' | sed -e '"+tc.script+"'")
		testutil.FailErr(t, "run sed text form", err)
		got := decodeOutcome(t, out)
		if got.Status != 0 || got.Stdout != tc.want {
			t.Fatalf("sed %q outcome: %+v", tc.script, got)
		}
	}
}

func TestProgramEntryReadPrefixIFSDoesNotPersist(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), "IFS= read -r line <<'EOF'\n  alpha beta  \nEOF\nprintf '<%s>|<%s>\\n' \"$line\" \"$IFS\"")
	testutil.FailErr(t, "read with prefix IFS", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || !strings.HasPrefix(got.Stdout, "<  alpha beta  >|") {
		t.Fatalf("read prefix outcome: %+v", got)
	}
}

func TestProgramEntryReadPrefixIFSRetainsEmptyFields(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), "IFS=: read -r first second <<'EOF'\n:alpha\nEOF\nprintf '<%s>|<%s>\\n' \"$first\" \"$second\"")
	testutil.FailErr(t, "read with non-whitespace IFS", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || got.Stdout != "<>|<alpha>\n" {
		t.Fatalf("read fields: %+v", got)
	}
}

func TestProgramEntryMissingExternalUsesShellStatus(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), "portable-command-missing-executable")
	testutil.FailErr(t, "run missing program", err)
	got := decodeOutcome(t, out)
	if got.Status != 127 || !strings.Contains(got.Stderr, "bash: portable-command-missing-executable: command not found") {
		t.Fatalf("missing program outcome: %+v", got)
	}
}

func TestProgramEntryBinarySearchContinuesWithoutMatchBudget(t *testing.T) {
	f := newProgramFixture(t)
	data := append([]byte("TODO\x00"), make([]byte, 300<<10)...)
	testutil.FailErr(t, "seed binary input", os.WriteFile(filepath.Join(f.Dir, "binary.dat"), data, 0o600))
	for _, program := range []string{"grep -rn TODO .", "rg -n TODO"} {
		out, err := f.call(t, t.Context(), program)
		testutil.FailErr(t, "run binary search", err)
		got := decodeOutcome(t, out)
		if got.Status != 0 || !strings.Contains(got.Stdout+got.Stderr, "Binary file ") || strings.Contains(got.Stderr, "GREP_MATCH_BUDGET") {
			t.Fatalf("%s: %+v", program, got)
		}
	}
}

func TestProgramEntryPipeStatusReportsClosedNativeProducer(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), `yes | head -1; printf '%s\n' "${PIPESTATUS[*]}"`)
	testutil.FailErr(t, "run closed-reader pipeline", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || got.Stdout != "y\n141 0\n" {
		t.Fatalf("pipeline status outcome: %+v", got)
	}
}
