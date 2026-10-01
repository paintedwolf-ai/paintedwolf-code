package governance

import (
	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"os"
	"testing"
)

// TestMain installs the bundled anchor registry these tests resolve anchors
// against.
func TestMain(m *testing.M) {
	anchortestsetup.Install()
	os.Exit(m.Run())
}
