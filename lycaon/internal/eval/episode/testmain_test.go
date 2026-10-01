package episode

import (
	"os"
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}
