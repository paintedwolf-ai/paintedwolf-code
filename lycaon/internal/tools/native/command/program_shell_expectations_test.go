package command_test

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

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
