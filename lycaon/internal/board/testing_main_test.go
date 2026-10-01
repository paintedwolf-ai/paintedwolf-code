package board

import (
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}
