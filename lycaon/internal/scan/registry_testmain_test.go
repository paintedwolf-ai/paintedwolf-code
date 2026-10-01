package scan

import (
	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	anchortestsetup.Install()
	os.Exit(m.Run())
}
