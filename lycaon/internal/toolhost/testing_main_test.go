package toolhost

import (
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	guidancetestsetup.Install()
	os.Exit(m.Run())
}
