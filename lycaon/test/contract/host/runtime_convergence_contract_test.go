package contract

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var runtimeConvergenceForbiddenInternalPatterns = []string{
	"CoordinatorImplementModeActive",
	"ScheduleWorkerTaskFinished",
	"AutoContinueWorkerTaskFinished",
	"ImplementDefaultAutoContinueAllowed",
	"implement_worker_await",
	"SetImplementWorkerAwaiter",
	"AwaitImplementWorkersAfterTask",
	"ContinuedAfterImplementWorkers",
	"TurnActive",
	"implement_auto_continue",
	"FinalizeTaskToolOutputAfterAwait",
	"IsPlanFamilyWorkflow",
	"OnWorkerTerminalReprompt",
	"RecordCoordinatorVerifyTurn",
	"SetVerifyTurnVars",
	"coordinator-implement.md",
}

func TestRuntimeConvergenceInternalGrepClean(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	for _, pattern := range runtimeConvergenceForbiddenInternalPatterns {
		out, err := exec.CommandContext(t.Context(), "rg", "-n", pattern, internal).CombinedOutput()
		if err == nil {
			t.Fatalf("forbidden pattern %q matched under lycaon/internal/:\n%s", pattern, strings.TrimSpace(string(out)))
		}
		if !isRgNoMatch(err) {
			testutil.FailErr(t, "rg "+pattern, err)
		}
	}
}

func isRgNoMatch(err error) bool {
	if err == nil {
		return false
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return true
	}
	return false
}
