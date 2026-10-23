package toolhost

import (
	"github.com/lycaon/lycaon/internal/gitengine"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	guidancetestsetup.Install()
	gitengine.TestingEnableBundledBinary()
	os.Exit(m.Run())
}
