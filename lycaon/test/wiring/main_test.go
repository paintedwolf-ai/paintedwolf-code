package wiring

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(RunReleasingHosts(m))
}
