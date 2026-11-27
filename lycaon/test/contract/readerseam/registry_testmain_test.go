package readerseam

import (
	"os"
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestMain(m *testing.M) {
	contractcheck.Setup()
	gittestsetup.Enable()
	wiring.PrepareHostProcess()
	os.Exit(m.Run())
}
