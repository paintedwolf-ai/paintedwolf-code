package conformance_test

import (
	"os"
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(wiring.RunReleasingHosts(m))
}
