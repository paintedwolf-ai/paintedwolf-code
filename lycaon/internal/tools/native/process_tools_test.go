package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestProcessListRequiresOwnerReview(t *testing.T) {
	service, err := hostprocess.New()
	testutil.FailErr(t, "create process service", err)
	native := ProcessTools{Service: service}
	denied := errors.New("review denied")
	calls := 0
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "task"},
		Execution: tools.InvocationExecution{ProcessReview: func(_ context.Context, operation string, processes []hostprocess.Process) error {
			calls++
			if operation != "list" || len(processes) != 0 {
				t.Fatal("unexpected review subject")
			}
			return denied
		}},
	}
	result, err := native.List(t.Context(), map[string]any{"pid": os.Getpid()}, tc)
	if !errors.Is(err, denied) || result != "" || calls != 1 {
		t.Fatalf("list bypassed review: %q %v %d", result, err, calls)
	}
	_, err = native.List(t.Context(), nil, tools.ToolContext{})
	if toolrejection.AsToolReject(err) == nil {
		t.Fatal("list without reviewer was not rejected")
	}
}

func TestProcessSignalSharesReferencesWithinTaskAndStopsAtReview(t *testing.T) {
	service, err := hostprocess.New()
	testutil.FailErr(t, "create process service", err)
	snapshot, err := service.List(t.Context(), "task", os.Getpid(), 0, 1)
	if errors.Is(err, hostprocess.ErrUnsupported) {
		t.Skip("native process operations unavailable")
	}
	testutil.FailErr(t, "inspect owned process", err)
	if len(snapshot.Processes) != 1 {
		t.Fatal("owned process unavailable")
	}
	denied := errors.New("review denied")
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "worker",
			ParentSessionID: "task"},
		Execution: tools.InvocationExecution{ProcessReview: func(_ context.Context, operation string, targets []hostprocess.Process) error {
			if operation != "signal" || len(targets) != 1 || targets[0].PID != os.Getpid() {
				t.Fatal("incorrect resolved review target")
			}
			return denied
		}},
	}
	native := ProcessTools{Service: service}
	args := map[string]any{"references": []any{snapshot.Processes[0].Reference}, "signal": "TERM"}
	_, err = native.Signal(t.Context(), args, tc)
	if !errors.Is(err, denied) {
		t.Fatalf("worker reference did not reach review: %v", err)
	}
	tc.Identity.ParentSessionID = "other-task"
	_, err = native.Signal(t.Context(), args, tc)
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "PROCESS_REFERENCE_STALE" {
		t.Fatalf("reference crossed task boundary: %v", err)
	}
}
