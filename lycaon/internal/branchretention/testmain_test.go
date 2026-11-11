package branchretention

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testvolume"
)

func TestMain(m *testing.M) {
	testvolume.SweepStrays()
	code := m.Run()
	testvolume.SweepStrays()
	os.Exit(code)
}
