package compaction

import (
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	guidancetestsetup.Install()
	testutil.VerifyNoLeaks(m, nil)
}
