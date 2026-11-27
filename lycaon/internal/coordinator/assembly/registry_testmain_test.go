package assembly

import (
	"os"
	"testing"

	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
)

func TestMain(m *testing.M) {
	anchortestsetup.Install()
	os.Exit(m.Run())
}
