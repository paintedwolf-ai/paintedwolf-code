package worker

import (
	"testing"

	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testvolume"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	anchortestsetup.Install()
	testvolume.SweepStrays()
	testutil.VerifyNoLeaks(m, func() { testvolume.SweepStrays() })
}
