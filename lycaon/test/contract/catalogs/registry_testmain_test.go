package contract

import (
	"os"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestMain(m *testing.M) {
	contractcheck.Setup()
	// The registry contract builds a whole host.
	wiring.PrepareHostProcess()
	os.Exit(m.Run())
}
