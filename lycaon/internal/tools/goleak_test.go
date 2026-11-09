package tools

import (
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// TestMain checks suite goroutine cleanup.
func TestMain(m *testing.M) {
	guidancetestsetup.Install()
	testutil.VerifyNoLeaks(m, nil)
}
