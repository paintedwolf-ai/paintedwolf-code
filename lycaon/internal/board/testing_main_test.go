package board

import (
	"github.com/lycaon/lycaon/internal/packboard"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	code := m.Run()
	packboard.WaitForProbes()
	os.Exit(code)
}
