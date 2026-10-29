package bgprocess

import (
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestMain checks suite goroutine cleanup.
func TestMain(m *testing.M) {
	gittestsetup.Enable()
	testutil.VerifyNoLeaks(m, nil)
}
