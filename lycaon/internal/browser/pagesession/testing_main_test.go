package pagesession

import (
	browsertestsetup "github.com/lycaon/lycaon/internal/testsetup/browser"
	"os"
	"testing"
)

// The harness isolates HOME, so the managed cache is invisible here; the
// checkout's staged browser is what these tests launch.
func TestMain(m *testing.M) {
	browsertestsetup.Enable()
	os.Exit(m.Run())
}
