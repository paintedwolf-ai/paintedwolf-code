package workspace

import (
	"os"
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testvolume"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	testvolume.SweepStrays()
	code := m.Run()
	testvolume.SweepStrays()
	os.Exit(code)
}
