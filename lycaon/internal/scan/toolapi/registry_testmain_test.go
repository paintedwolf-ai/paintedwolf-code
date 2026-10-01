package toolapi

import (
	"os"
	"testing"

	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	anchortestsetup.Install()
	os.Exit(m.Run())
}
