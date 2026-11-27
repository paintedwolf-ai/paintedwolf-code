package contract

import (
	"os"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func TestMain(m *testing.M) {
	contractcheck.Setup()
	release := hoststate.HoldAuthorityMemos()
	code := m.Run()
	release()
	os.Exit(code)
}
