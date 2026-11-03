package command_test

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProcessAgeSelectionDoesNotClaimUnobservedNativeJob(t *testing.T) {
	f := newProgramFixture(t)
	f.Runtime.Owners = func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		return command.ProgramOwners{Natives: map[string]command.NativeOperation{"head": {
			Tool: "read",
			Prepare: func(_ context.Context, _ *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				bound, err := commandprogram.CatalogBinder{}.Bind(catalogSpec("head"), request.Argv)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Review: bound.Review("read"), Invoke: func(ctx context.Context, _ tools.ToolContext, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
					<-ctx.Done()
					return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, ctx.Err()
				}}, nil
			},
		}}}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	out, err := f.call(t, ctx, `head -n 7391 & job=$!; pgrep -n -f -x 'head -n 7391'; found=$?; pkill -o -f -x 'head -n 7391'; stopped=$?; kill "$job"; wait "$job"; waited=$?; external -- /bin/echo "found=$found stopped=$stopped waited=$waited"`)
	testutil.FailErr(t, "select native job without OS age", err)
	got := decodeOutcome(t, out)
	if got.Status != 0 || got.Stdout != "found=1 stopped=1 waited=143\n" || got.Stderr != "" {
		t.Fatalf("unobserved-age selection changed the job: %+v", got)
	}
}

func TestProgramShellProcessInspectionAndOwnership(t *testing.T) {
	f := newProgramFixture(t)

	// Start a foreign host process outside the session so ps/pgrep see it
	// read-only, while kill/pkill refuse it with PROCESS_NOT_OWNED.
	foreignCmd := exec.Command("/bin/sleep", "30")
	testutil.FailErr(t, "start foreign process", foreignCmd.Start())
	t.Cleanup(func() {
		_ = foreignCmd.Process.Kill()
		_ = foreignCmd.Wait()
	})
	foreignPID := strconv.Itoa(foreignCmd.Process.Pid)

	// Launch a session-owned external background job; $! must be its real OS PID
	// and accepted by kill and wait, while ps aux, ps -ef and pgrep -f list both
	// processes and mark the session's own job.
	prog := fmt.Sprintf(
		`external -- /bin/sleep 30 & bgpid=$!; `+
			`external -- /bin/echo "bgpid=$bgpid"; `+
			`ps aux | grep %s; `+
			`ps -ef | grep "$bgpid"; `+
			`pgrep -f -a "sleep 30"; `+
			`kill "$bgpid"; wait "$bgpid"; external -- /bin/echo "waited=$?"`,
		foreignPID,
	)
	out, err := f.call(t, t.Context(), prog)
	testutil.FailErr(t, "run process inspection and $! lifecycle", err)
	outcome := decodeOutcome(t, out)
	if outcome.Refusal != nil || outcome.Status != 0 {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	var bgPID string
	for _, line := range strings.Split(outcome.Stdout, "\n") {
		if after, ok := strings.CutPrefix(line, "bgpid="); ok {
			bgPID = strings.TrimSpace(after)
			break
		}
	}
	if bgPID == "" {
		t.Fatalf("missing bgpid in stdout: %q", outcome.Stdout)
	}
	if pidNum, convErr := strconv.Atoi(bgPID); convErr != nil || pidNum <= 0 {
		t.Fatalf("expected $! for external background job to be numeric OS PID, got %q", bgPID)
	}
	if !strings.Contains(outcome.Stdout, foreignPID) {
		t.Fatalf("expected ps aux to see foreign process %s in %q", foreignPID, outcome.Stdout)
	}
	if !strings.Contains(outcome.Stdout, "[session job") {
		t.Fatalf("expected ps/pgrep output to mark session-owned job in %q", outcome.Stdout)
	}
	if !strings.Contains(outcome.Stdout, "waited=143") {
		t.Fatalf("expected kill $! and wait $! to settle with 143 in %q", outcome.Stdout)
	}

	// Signalling a live non-session process via kill or pkill must refuse with PROCESS_NOT_OWNED.
	killForeignOut, err := f.call(t, t.Context(), "kill "+foreignPID)
	testutil.FailErr(t, "kill foreign process", err)
	killForeign := decodeOutcome(t, killForeignOut)
	if killForeign.Refusal == nil || killForeign.Refusal.Code != "PROCESS_NOT_OWNED" || !strings.Contains(killForeign.Stderr, "PROCESS_NOT_OWNED") {
		t.Fatalf("expected PROCESS_NOT_OWNED for kill of foreign PID %s, got %+v", foreignPID, killForeign)
	}

	pkillForeignOut, err := f.call(t, t.Context(), `pkill -f "sleep 30"`)
	testutil.FailErr(t, "pkill foreign process", err)
	pkillForeign := decodeOutcome(t, pkillForeignOut)
	if pkillForeign.Refusal == nil || pkillForeign.Refusal.Code != "PROCESS_NOT_OWNED" || !strings.Contains(pkillForeign.Stderr, "PROCESS_NOT_OWNED") {
		t.Fatalf("expected PROCESS_NOT_OWNED for pkill matching only foreign process, got %+v", pkillForeign)
	}

	// pkill on a session-owned job succeeds.
	pkillOwnedOut, err := f.call(t, t.Context(), `external -- /bin/sleep 30 & pid=$!; pkill -f "sleep 30"; wait "$pid"; external -- /bin/echo "pkilled=$?"`)
	testutil.FailErr(t, "pkill owned process", err)
	pkillOwned := decodeOutcome(t, pkillOwnedOut)
	if pkillOwned.Refusal != nil || !strings.Contains(pkillOwned.Stdout, "pkilled=143") {
		t.Fatalf("expected pkill on owned process to settle with 143, got %+v", pkillOwned)
	}
}

func TestProgramPsCustomHeadersAndUnknownField(t *testing.T) {
	f := newProgramFixture(t)
	pid := strconv.Itoa(os.Getpid())
	for _, tc := range []struct{ args, stdout string }{
		{"-o 'pid=Process number' -o uid=Owner", fmt.Sprintf("Process number Owner\n%s %d\n", pid, os.Getuid())},
		{"-o pid=Number -o uid=Owner", fmt.Sprintf("Number Owner\n%s %d\n", pid, os.Getuid())},
		{"-o pid= -o uid=", fmt.Sprintf("%s %d\n", pid, os.Getuid())},
	} {
		out, err := f.call(t, t.Context(), "ps -p "+pid+" "+tc.args)
		testutil.FailErr(t, "custom ps output", err)
		got := decodeOutcome(t, out)
		if got.Status != 0 || got.Stdout != tc.stdout {
			t.Fatalf("ps %s: %+v; want %q", tc.args, got, tc.stdout)
		}
	}
	out, err := f.call(t, t.Context(), "ps -p "+pid+" -o nonsense")
	testutil.FailErr(t, "invalid ps field", err)
	got := decodeOutcome(t, out)
	if got.Status == 0 || got.Stdout != "" || got.Stderr != "ps: unknown output field: nonsense\n" {
		t.Fatalf("unknown field outcome: %+v", got)
	}
}
