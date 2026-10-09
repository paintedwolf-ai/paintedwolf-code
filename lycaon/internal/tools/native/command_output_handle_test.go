package native

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
)

func TestMissingCommandHandleRejectNoLiveJob(t *testing.T) {
	reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	err := missingCommandHandleReject(reg, "sess", "command-1")
	tr := toolrejection.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_OUTPUT_NO_LIVE_JOB" {
		t.Fatalf("got %v", err)
	}
	if got, _ := tr.Data["handle"].(string); got != "command-1" {
		t.Fatalf("handle = %q", got)
	}
}

func TestMissingCommandHandleRejectNilRegistry(t *testing.T) {
	err := missingCommandHandleReject(nil, "sess", "command-1")
	tr := toolrejection.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_OUTPUT_NO_LIVE_JOB" {
		t.Fatalf("got %v", err)
	}
}

func TestCommandCapacityUsesRefusedAdmission(t *testing.T) {
	err := &bgprocess.AwaitedCapacityError{Limit: 2, Handles: []string{"a", "b"}}
	reject := toolrejection.AsToolReject(commandStartError(err))
	if reject == nil || reject.Code != "COMMAND_CONCURRENCY_CAP_REACHED" || reject.Data["max_awaited"] != 2 || reject.Data["count"] != 2 {
		t.Fatalf("configured admission facts lost: %+v", reject)
	}
	handles, ok := reject.Data["live_command_handles"].([]string)
	if !ok || len(handles) != 2 || handles[0] != "a" || handles[1] != "b" {
		t.Fatalf("owned admission handles lost: %+v", reject.Data)
	}
}
