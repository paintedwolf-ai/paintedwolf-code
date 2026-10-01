package worker

import (
	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	anchortestsetup.Install()
	testutil.VerifyNoLeaks(m, nil)
}
