package contract

import (
	"os"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestMain(m *testing.M) {
	contractcheck.Setup()
	os.Exit(m.Run())
}
