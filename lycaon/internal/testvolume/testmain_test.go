package testvolume

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	SweepStrays()
	code := m.Run()
	SweepStrays()
	os.Exit(code)
}
