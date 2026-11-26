package workflowadmin_test

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/test/wiring"

	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
)

// TestMain installs process-global test dependencies.
func TestMain(m *testing.M) {
	gittestsetup.Enable()
	guidancetestsetup.Install()
	anchortestsetup.Install()
	os.Exit(wiring.Main(m))
}
