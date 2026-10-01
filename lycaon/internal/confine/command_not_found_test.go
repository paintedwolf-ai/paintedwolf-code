package confine

import (
	"errors"
	"testing"
)

func TestCommandNotFoundErrorMessage(t *testing.T) {
	err := &CommandNotFoundError{Name: "missing-bin"}
	if got := err.Error(); got != `command not found: "missing-bin"` {
		t.Fatalf("Error() = %q", got)
	}
	if !IsCommandNotFound(err) {
		t.Fatal("IsCommandNotFound direct")
	}
	if !IsCommandNotFound(errors.Join(errors.New("wrap"), err)) {
		t.Fatal("IsCommandNotFound wrapped")
	}
	if IsCommandNotFound(errors.New("other")) {
		t.Fatal("false positive")
	}
}

// The helper writes the line; ReportsCommandNotFound reads it back. They are
// pinned together here because they are separated at runtime by a process
// boundary, where nothing else would catch one of them changing.
func TestReportsCommandNotFoundReadsWhatTheHelperWrites(t *testing.T) {
	line := HelperStderrPrefix + (&CommandNotFoundError{Name: "missing-bin"}).Error()
	if !ReportsCommandNotFound(CommandNotFoundExit, "some earlier output\n"+line+"\n") {
		t.Fatalf("helper line %q not recognized", line)
	}
	if ReportsCommandNotFound(0, line) {
		t.Fatal("exit 0 with the line must not report a missing command")
	}
	if ReportsCommandNotFound(CommandNotFoundExit, "exited 127 for its own reasons") {
		t.Fatal("exit 127 alone must not report a missing command")
	}
	if ReportsCommandNotFound(CommandNotFoundExit, "") {
		t.Fatal("a tail that lost the line must not report a missing command")
	}
}
