package promptstate

import (
	"testing"

	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestMain installs the shared git, guidance, and anchor fixtures and checks for leaks.
func TestMain(m *testing.M) {
	gittestsetup.Enable()
	guidancetestsetup.Install()
	anchortestsetup.Install()
	testutil.VerifyNoLeaks(m, nil)
}
