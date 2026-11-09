package sessions

import (
	"os"
	"testing"

	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
)

func TestMain(m *testing.M) {
	guidancetestsetup.Install()
	os.Exit(m.Run())
}
