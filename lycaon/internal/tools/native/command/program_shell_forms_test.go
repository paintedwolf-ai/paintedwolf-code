package command_test

import (
	"fmt"
	"os"
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

func TestProgramEntryJobsLongAndPsUseNumericBackgroundPID(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), `external -- /bin/sleep 30 & pid=$!; jobs -l; ps -p "$pid" -o pid=; kill "$pid"; wait "$pid"`)
	testutil.FailErr(t, "inspect background process", err)
	got := decodeOutcome(t, out)
	if got.Status != 143 || !strings.Contains(got.Stdout, "[1] ") {
		t.Fatalf("background process outcome: %+v", got)
	}
	lines := strings.Split(strings.TrimSpace(got.Stdout), "\n")
	if len(lines) < 2 {
		t.Fatalf("missing jobs or ps output: %q", got.Stdout)
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 3 || fields[1] != strings.TrimSpace(lines[1]) {
		t.Fatalf("jobs and ps disagree on PID: %q", got.Stdout)
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

func TestProgramEntryPipeStatusReportsClosedProducer(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), `yes | head -1; printf '%s\n' "${PIPESTATUS[*]}"`)
	testutil.FailErr(t, "run closed-reader pipeline", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || got.Stdout != "y\n141 0\n" {
		t.Fatalf("pipeline status outcome: %+v", got)
	}
}
