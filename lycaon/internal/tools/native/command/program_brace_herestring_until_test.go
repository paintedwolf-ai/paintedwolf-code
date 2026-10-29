package command_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgramEntryBraceWordsRunEachSubstitutionPerWord(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), `printf '%s\n' {a,b}$(printf c) x{1..2}`)
	testutil.FailErr(t, "run brace substitutions", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || got.Stdout != "ac\nbc\nx1\nx2\n" {
		t.Fatalf("brace outcome: %+v", got)
	}
	if programs := f.programRows(t); len(programs) != 1 || programs[0] != "completed" {
		t.Fatalf("program receipts %v", programs)
	}
}

func TestProgramEntryUntilAndHereStringShareTheLoopAndStdinPaths(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), `i=0; until [ "$i" -ge 2 ]; do i=$((i+1)); read -r line <<< "line $i"; printf '%s\n' "$line"; done; : ${DONE:=yes}; printf '%s\n' "$DONE"`)
	testutil.FailErr(t, "run until with here-strings", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || got.Stdout != "line 1\nline 2\nyes\n" {
		t.Fatalf("until outcome: %+v", got)
	}
}
