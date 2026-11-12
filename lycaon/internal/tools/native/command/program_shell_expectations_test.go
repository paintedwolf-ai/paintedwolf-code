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

	// Start a foreign host process outside the session: ps and pgrep list only
	// the session's own processes, pkill finds nothing to match, and kill of
	// its named PID refuses with PROCESS_NOT_OWNED.
	foreignCmd := exec.Command("/bin/sleep", "30")
	testutil.FailErr(t, "start foreign process", foreignCmd.Start())
	t.Cleanup(func() {
		_ = foreignCmd.Process.Kill()
		_ = foreignCmd.Wait()
	})
	foreignPID := strconv.Itoa(foreignCmd.Process.Pid)

	// Launch a session-owned external background job; $! must be its real OS PID
	// and accepted by kill and wait, while ps aux, ps -ef and pgrep -f list it
	// as the session's own job and omit the foreign process.
	// The pipeline's own grep is a session member whose argv contains the
	// pattern, and under load it can exec before the ps builtin snapshots the
	// session. Bracketing the pattern's first digit keeps that self-row from
	// matching, while a listing of the foreign process still matches through
	// its PID column.
	prog := fmt.Sprintf(
		`external -- /bin/sleep 30 & bgpid=$!; `+
			`external -- /bin/echo "bgpid=$bgpid"; `+
			`ps aux | grep "[%c]%s"; `+
			`ps -ef | grep "$bgpid"; `+
			`pgrep -f -a "sleep 30"; `+
			`kill "$bgpid"; wait "$bgpid"; external -- /bin/echo "waited=$?"`,
		foreignPID[0], foreignPID[1:],
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
	if strings.Contains(outcome.Stdout, foreignPID) {
		t.Fatalf("a native listing showed foreign process %s: %q", foreignPID, outcome.Stdout)
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
	if pkillForeign.Refusal != nil || pkillForeign.Status != 1 || strings.Contains(pkillForeign.Stderr, foreignPID) {
		t.Fatalf("pkill matching only a foreign process must find nothing, got %+v", pkillForeign)
	}

	// pkill on a session-owned job succeeds.
	pkillOwnedOut, err := f.call(t, t.Context(), `external -- /bin/sleep 30 & pid=$!; pkill -f "sleep 30"; wait "$pid"; external -- /bin/echo "pkilled=$?"`)
	testutil.FailErr(t, "pkill owned process", err)
	pkillOwned := decodeOutcome(t, pkillOwnedOut)
	if pkillOwned.Refusal != nil || !strings.Contains(pkillOwned.Stdout, "pkilled=143") {
		t.Fatalf("expected pkill on owned process to settle with 143, got %+v", pkillOwned)
	}

	// A process a job started is the session's too: pkill signals that child
	// alone, by its kernel instance, and the job carries on.
	childOut, err := f.call(t, t.Context(), `external -- /bin/sh -c '/bin/sleep 31; echo after=$?' & job=$!; i=0; until pgrep -f -x '/bin/sleep 31' >/dev/null || [ "$i" -ge 100 ]; do i=$((i+1)); external -- /bin/sleep 0.1; done; pkill -f -x '/bin/sleep 31'; external -- /bin/echo "pkill=$?"; wait "$job"; external -- /bin/echo "job=$?"`)
	testutil.FailErr(t, "pkill a job's child", err)
	child := decodeOutcome(t, childOut)
	if child.Refusal != nil || !strings.Contains(child.Stdout, "pkill=0") || !strings.Contains(child.Stdout, "after=143") || !strings.Contains(child.Stdout, "job=0") {
		t.Fatalf("pkill of a job's child = %+v", child)
	}
}

func TestProgramPsCustomHeadersAndUnknownField(t *testing.T) {
	f := newProgramFixture(t)
	uid := strconv.Itoa(os.Getuid())
	for _, tc := range []struct{ args, header string }{
		{"-o 'pid=Process number' -o uid=Owner", "Process number Owner\n"},
		{"-o pid=Number -o uid=Owner", "Number Owner\n"},
		{"-o pid= -o uid=", ""},
	} {
		out, err := f.call(t, t.Context(), `external -- /bin/sleep 30 & p=$!; external -- /bin/echo "pid=$p"; ps -p "$p" `+tc.args+`; kill "$p"; wait "$p"`)
		testutil.FailErr(t, "custom ps output", err)
		got := decodeOutcome(t, out)
		pidLine, rest, _ := strings.Cut(got.Stdout, "\n")
		pid := strings.TrimPrefix(pidLine, "pid=")
		if want := tc.header + pid + " " + uid + "\n"; rest != want {
			t.Fatalf("ps %s: %+v; want %q after the pid line", tc.args, got, want)
		}
	}
	out, err := f.call(t, t.Context(), `external -- /bin/sleep 30 & p=$!; ps -p "$p" -o nonsense; s=$?; kill "$p"; wait "$p"; exit $s`)
	testutil.FailErr(t, "invalid ps field", err)
	got := decodeOutcome(t, out)
	if got.Status == 0 || got.Stdout != "" || got.Stderr != "ps: unknown output field: nonsense\n" {
		t.Fatalf("unknown field outcome: %+v", got)
	}
}
