package webresearch

import (
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func TestMain(m *testing.M) {
	guidancetestsetup.Install()
	testutil.VerifyNoLeaks(m, nil)
}
