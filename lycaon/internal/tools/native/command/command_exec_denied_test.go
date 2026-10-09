package command

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestRejectCommandExecDeniedFromConfineExit(t *testing.T) {
	res := &hostcmd.Result{
		ExitCode: 126,
		Tail:     `confine: exec denied: "./ntp_check.py"`,
		OK:       false,
	}
	err := rejectCommandExecDenied(res, map[string]any{"command": "./ntp_check.py --json"})
	tr := tools.AsToolReject(err)
	if tr == nil {
		t.Fatalf("want ToolReject, got %v", err)
	}
	if tr.Code != "COMMAND_EXEC_DENIED" {
		t.Fatalf("code = %q", tr.Code)
	}
	if got, _ := tr.Data["path"].(string); got != "./ntp_check.py" {
		t.Fatalf("path = %q", got)
	}
}

func TestRejectCommandExecDeniedIgnoresOtherExit126(t *testing.T) {
	res := &hostcmd.Result{ExitCode: 126, Tail: "confine: empty sandbox profile", OK: false}
	if err := rejectCommandExecDenied(res, map[string]any{"command": "./tool"}); err != nil {
		t.Fatalf("non-exec-denied 126 must not reject: %v", err)
	}
}

func TestRejectCommandExecDeniedIgnoresPermissionProse(t *testing.T) {
	res := &hostcmd.Result{ExitCode: 126, Tail: "permission denied", OK: false}
	if err := rejectCommandExecDenied(res, map[string]any{"command": "./tool"}); err != nil {
		t.Fatalf("generic permission denied must not reject: %v", err)
	}
}

func TestRejectStartCommandExecDenied(t *testing.T) {
	err := rejectStartCommandExecDenied(&confine.ExecDeniedError{Path: "./script"})
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_EXEC_DENIED" {
		t.Fatalf("got %v", err)
	}
	if got, _ := tr.Data["path"].(string); got != "./script" {
		t.Fatalf("path = %q", got)
	}
}

func TestRejectStartCommandExecDeniedIgnoresOther(t *testing.T) {
	if err := rejectStartCommandExecDenied(errors.New("boom")); err != nil {
		t.Fatalf("unexpected reject: %v", err)
	}
}
