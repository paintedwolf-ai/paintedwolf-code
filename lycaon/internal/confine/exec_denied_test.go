package confine

import (
	"errors"
	"io/fs"
	"syscall"
	"testing"
)

func asExecDenied(err error) bool {
	var denied *ExecDeniedError
	return errors.As(err, &denied)
}

func TestReportsExecDeniedReadsTheHelperMarker(t *testing.T) {
	line := HelperStderrPrefix + (&ExecDeniedError{Path: "./ntp_check.py"}).Error()
	if !ReportsExecDenied(HelperFailureExit, "earlier output\n"+line+"\n") {
		t.Fatalf("helper line %q not reported", line)
	}
	if ReportsExecDenied(HelperFailureExit, HelperStderrPrefix+"empty sandbox profile") {
		t.Fatal("another helper failure reported as exec denied")
	}
	if ReportsExecDenied(1, line) {
		t.Fatal("exit status other than the helper's reported as exec denied")
	}
}

func TestClassifyExecErrorAccessDenied(t *testing.T) {
	got := ClassifyExecError("./script", syscall.EACCES)
	if !asExecDenied(got) {
		t.Fatalf("EACCES = %v", got)
	}
	if got.Error() != `exec denied: "./script"` {
		t.Fatalf("Error() = %q", got)
	}
	if got := ClassifyExecError("./script", fs.ErrPermission); !asExecDenied(got) {
		t.Fatalf("fs.ErrPermission = %v", got)
	}
}

func TestClassifyExecErrorOtherStaysWrapped(t *testing.T) {
	got := ClassifyExecError("./script", syscall.ENOENT)
	if asExecDenied(got) {
		t.Fatal("ENOENT must not classify as exec denied")
	}
	if got == nil || got.Error() == `exec denied: "./script"` {
		t.Fatalf("want wrapped exec error, got %v", got)
	}
}
