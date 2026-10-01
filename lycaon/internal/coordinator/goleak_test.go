package coordinator

import (
	"github.com/lycaon/lycaon/internal/repochange"
	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

// TestMain checks suite goroutine cleanup.
func TestMain(m *testing.M) {
	anchortestsetup.Install()
	testutil.VerifyNoLeaks(m, repochange.ResetWatchersForTest)
}
