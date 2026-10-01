package llm

import (
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Change-set assembly reads git state, which resolves only beside the running
// executable — a test binary has no engine staged next to it.
func TestMain(m *testing.M) {
	gittestsetup.Enable()
	guidancetestsetup.Install()
	testutil.VerifyNoLeaks(m, nil)
}
