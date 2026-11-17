package commands

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/test/wiring"
)

func TestMain(m *testing.M) {
	os.Exit(wiring.Main(m))
}
