package conformance_test

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/test/wiring"
)

func TestMain(m *testing.M) {
	setReferenceUmask()
	os.Exit(wiring.Main(m))
}
