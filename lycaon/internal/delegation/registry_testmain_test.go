package delegation_test

import (
	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"os"
	"testing"
)

// The anchor Binding registry is a process-global the serve builder installs at
// boot. Without it a worker fails its task preamble and the leg gives up yet
// still reaches phase done, so dispatch tests would pass vacuously.
func TestMain(m *testing.M) {
	anchortestsetup.Install()
	os.Exit(m.Run())
}
