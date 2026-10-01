package sourceview

import (
	"os"
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
)

// TestMain installs process-global test dependencies.
func TestMain(m *testing.M) {
	gittestsetup.Enable()
	guidancetestsetup.Install()
	os.Exit(m.Run())
}
