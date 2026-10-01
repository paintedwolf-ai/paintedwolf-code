package git

import (
	"github.com/lycaon/lycaon/internal/repochange"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	// Status cache tests count loads; a watcher event invalidates one mid-load.
	repochange.DisableWatchersForTest()
	os.Exit(m.Run())
}
