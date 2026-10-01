package contract

import (
	"os"
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestMain(m *testing.M) {
	contractcheck.Setup()
	gittestsetup.Enable()
	os.Exit(m.Run())
}
