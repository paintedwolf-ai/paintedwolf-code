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

func TestProgramEntryDefaultIFSAndScopedPrefix(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), `printf 'a,b\n' | { IFS=, read -r x y; printf '%s/%s\n' "$x" "$y"; printf 'ifs:%s\n' "${#IFS}"; }; printf '[%s]\n' "$IFS"`)
	testutil.FailErr(t, "read with a prefix IFS", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || got.Stdout != "a/b\nifs:3\n[ \t\n]\n" {
		t.Fatalf("default IFS outcome: %+v", got)
	}
}

func TestProgramEntryEvalSyntaxRefusalWritesShellLine(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), `printf 'before\n'; eval 'if'`)
	testutil.FailErr(t, "run invalid eval source", err)
	got := decodeOutcome(t, out)
	const stderr = "eval: line 1: The complete submitted or captured syntax unit is malformed or has an unsupported source encoding [COMMAND_SYNTAX_INVALID]\n"
	if got.Refusal == nil || got.Refusal.Code != "COMMAND_SYNTAX_INVALID" || got.Status != 2 || got.Stdout != "before\n" || got.Stderr != stderr {
		t.Fatalf("eval refusal outcome: %+v, want stderr %q", got, stderr)
	}
}

func TestProgramEntryKilledJobsSettleTheProgram(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), `sleep 30 & p=$!; kill "$p"; wait "$p"; printf 'pid:%s\n' "$?"; sleep 30 & kill %1; wait %1; printf 'job:%s\n' "$?"`)
	testutil.FailErr(t, "kill and wait jobs", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || got.Stdout != "pid:143\njob:143\n" || got.State != "completed" {
		t.Fatalf("killed jobs outcome: %+v", got)
	}
}

// A native producer ended by its reader settles as SIGPIPE in PIPESTATUS, and
// the pipeline itself succeeds.
func TestProgramEntryPsClosedByItsReaderSucceeds(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), `ps aux | true; printf '%s %s\n' "$?" "${PIPESTATUS[*]}"`)
	testutil.FailErr(t, "run ps into a closed reader", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || got.Stdout != "0 141 0\n" {
		t.Fatalf("closed ps pipeline outcome: %+v", got)
	}
}
