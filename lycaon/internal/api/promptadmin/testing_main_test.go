package promptadmin

import (
	"os"
	"testing"

	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
)

// TestMain installs process-global test dependencies.
func TestMain(m *testing.M) {
	gittestsetup.Enable()
	guidancetestsetup.Install()
	anchortestsetup.Install()
	os.Exit(m.Run())
}
