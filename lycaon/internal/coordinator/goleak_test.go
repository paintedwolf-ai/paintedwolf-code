package coordinator

import (
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"

	"testing"

	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestMain checks suite goroutine cleanup.
func TestMain(m *testing.M) {
	releaseWatchers := repochangetest.Suite()
	anchortestsetup.Install()
	testutil.VerifyNoLeaks(m, releaseWatchers)
}
