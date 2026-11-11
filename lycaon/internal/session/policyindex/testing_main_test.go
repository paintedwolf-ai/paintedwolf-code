package policyindex

import (
	"testing"

	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMain(m *testing.M) {
	anchortestsetup.Install()
	testutil.VerifyNoLeaks(m, nil)
}
