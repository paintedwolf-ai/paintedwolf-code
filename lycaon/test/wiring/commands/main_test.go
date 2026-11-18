package commands

import (
	"os"
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestMain(m *testing.M) {
	testutil.SelectRaceTests(
		"TestWorkflowBackgroundBuildWaitAndStatusCheck",
	)
	gittestsetup.Enable()
	os.Exit(wiring.Main(m))
}
