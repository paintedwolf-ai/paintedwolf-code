package reenter_test

import (
	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	anchortestsetup.Install()
	os.Exit(m.Run())
}
