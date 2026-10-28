package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
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

// References are bound to the session whose launch started the process; the
// review sees the host-resolved instance, and nothing else reaches review.
func TestProcessSignalReferencesStayWithTheOwningSessionAndStopAtReview(t *testing.T) {
	service, err := hostprocess.New()
	testutil.FailErr(t, "create process service", err)
	line, err := lineage.Open("command:call_1", "worker")
	testutil.FailErr(t, "open lineage", err)
	t.Cleanup(func() { _ = line.Close() })
	child := exec.Command("/bin/sleep", "60")
	child.ExtraFiles = []*os.File{line.ChildFile()}
	testutil.FailErr(t, "start owned child", child.Start())
	line.Started()
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	unowned := exec.Command("/bin/sleep", "60")
	testutil.FailErr(t, "start unowned child", unowned.Start())
	t.Cleanup(func() { _ = unowned.Process.Kill(); _ = unowned.Wait() })

	denied := errors.New("review denied")
	reviewed := 0
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "worker", ParentSessionID: "task"},
		Execution: tools.InvocationExecution{ProcessReview: func(_ context.Context, operation string, targets []hostprocess.Process) error {
			if operation == "list" {
				return nil
			}
			reviewed++
			if operation != "signal" || len(targets) != 1 || targets[0].PID != child.Process.Pid || !targets[0].Owned {
				t.Fatal("incorrect resolved review target")
			}
			return denied
		}},
	}
	native := ProcessTools{Service: service}
	reference := ""
	for _, pid := range []int{child.Process.Pid, unowned.Process.Pid} {
		raw, err := native.List(t.Context(), map[string]any{"pid": pid}, tc)
		testutil.FailErr(t, "list process", err)
		var listed struct{ Processes []hostprocess.Process }
		testutil.FailErr(t, "decode listing", json.Unmarshal([]byte(raw), &listed))
		if len(listed.Processes) != 1 {
			t.Fatalf("listing %s", raw)
		}
		row := listed.Processes[0]
		if pid == unowned.Process.Pid {
			if row.Owned || row.Reference != "" {
				t.Fatalf("a process the session did not start carries a reference: %s", raw)
			}
			continue
		}
		if !row.Owned || row.Reference == "" {
			t.Fatalf("the session's own process carries no reference: %s", raw)
		}
		reference = row.Reference
	}
	args := map[string]any{"references": []any{reference}, "signal": "TERM"}
	if _, err := native.Signal(t.Context(), args, tc); !errors.Is(err, denied) || reviewed != 1 {
		t.Fatalf("owned reference did not reach review: %v", err)
	}
	tc.Identity.SessionID = "task"
	_, err = native.Signal(t.Context(), args, tc)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "PROCESS_REFERENCE_STALE" || reviewed != 1 {
		t.Fatalf("reference crossed the session boundary: %v", err)
	}
	if reject := toolrejection.AsToolReject(processError(hostprocess.ErrNotOwned)); reject == nil || reject.Code != "PROCESS_NOT_OWNED" {
		t.Fatalf("unowned refusal code: %v", reject)
	}
}
