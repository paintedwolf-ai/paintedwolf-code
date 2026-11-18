package guard

import (
	"os"
	"testing"

	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
)

func TestMain(m *testing.M) {
	// The anchor registry ships in the binary, so it loads without locating the
	// module root first.
	anchortestsetup.Install()
	os.Exit(m.Run())
}
