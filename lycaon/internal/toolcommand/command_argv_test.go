package toolcommand

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
)

func TestArgvShapeObservationCommandRequired(t *testing.T) {
	tr := ArgvShapeObservation("command", commandsurface.ErrArgvRequired)
	if tr == nil || tr.Code != "COMMAND_ARGV_REQUIRED" {
		t.Fatalf("got %#v", tr)
	}
}

func TestArgvShapeObservationVerifyRequired(t *testing.T) {
	tr := ArgvShapeObservation("verify", commandsurface.ErrArgvRequired)
	if tr == nil || tr.Code != "VERIFY_COMMAND_UNDECLARED" {
		t.Fatalf("got %#v", tr)
	}
}

func TestArgvShapeObservationConflict(t *testing.T) {
	for _, tool := range []string{"command", "verify"} {
		tr := ArgvShapeObservation(tool, commandsurface.ErrArgvConflict)
		if tr == nil || tr.Code != "COMMAND_ARGV_CONFLICT" {
			t.Fatalf("%s: got %#v", tool, tr)
		}
	}
}

func TestArgvShapeObservationPipelineShape(t *testing.T) {
	tr := ArgvShapeObservation("command", commandsurface.ErrPipelineShape)
	if tr == nil || tr.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("got %#v", tr)
	}
}

func TestArgvShapeObservationIgnoresOtherErrors(t *testing.T) {
	if tr := ArgvShapeObservation("command", errors.New("not argv shape")); tr != nil {
		t.Fatalf("got %#v", tr)
	}
	if tr := ArgvShapeObservation("read", commandsurface.ErrArgvRequired); tr != nil {
		t.Fatalf("got %#v", tr)
	}
	if tr := ArgvShapeObservation("command", nil); tr != nil {
		t.Fatalf("got %#v", tr)
	}
}

func TestArgvShapeRejectCommandMissing(t *testing.T) {
	tr := ArgvShapeReject("command", map[string]any{"cwd": ".", "timeout_ms": 120000})
	if tr == nil || tr.Code != "COMMAND_ARGV_REQUIRED" {
		t.Fatalf("got %#v", tr)
	}
}

func TestArgvShapeRejectVerifyMissing(t *testing.T) {
	tr := ArgvShapeReject("verify", map[string]any{"cwd": "."})
	if tr == nil || tr.Code != "VERIFY_COMMAND_UNDECLARED" {
		t.Fatalf("got %#v", tr)
	}
}

func TestArgvShapeRejectConflict(t *testing.T) {
	args := map[string]any{"command": "go version", "pipeline": []any{"echo hi"}}
	tr := ArgvShapeReject("command", args)
	if tr == nil || tr.Code != "COMMAND_ARGV_CONFLICT" {
		t.Fatalf("got %#v", tr)
	}
}
