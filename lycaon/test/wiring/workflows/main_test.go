package workflows

import (
	"os"
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestMain(m *testing.M) {
	testutil.SelectRaceTests(
		"TestCoordinatorParallelWorkersReplaySiblingNotes",
		"TestCoordinatorParallelWorkersReplayRootSessionKeyParity",
		"TestCoordinatorParallelWorkersReplayOverlayPathReadForbidden",
		"TestCoordinatorParallelWorkersReplayLineShiftAtomicRejection",
		"TestCoordinatorParallelWorkersReplayPriorRunFindingsExcluded",
		"TestCoordinatorParallelTaskCapMixedAgents",
		"TestCoordinatorTaskScopeDispatchReadFanOutParallelWriteAllowed",
		"TestWorkflowVersionDualRun_Concurrent100And200Isolation",
	)
	gittestsetup.Enable()
	os.Exit(wiring.Main(m))
}
