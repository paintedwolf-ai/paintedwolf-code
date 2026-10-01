package browser

import (
	browsertestsetup "github.com/lycaon/lycaon/internal/testsetup/browser"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The harness isolates HOME; these tests launch the checkout's staged browser, or the
// pinned browser provisioned under the developer's real home.
func TestMain(m *testing.M) {
	browsertestsetup.Enable()
	testutil.VerifyNoLeaks(m, nil)
}
