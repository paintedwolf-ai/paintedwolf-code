package wiring

import (
	"os"
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.SelectRaceTests(
		"TestContentApplyHoldsEveryContentMutatingTool",
	)
	gittestsetup.Enable()
	os.Exit(Main(m))
}
