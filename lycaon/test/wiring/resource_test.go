package wiring

import (
	"github.com/lycaon/lycaon/internal/testutil/resourceguard"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(resourceguard.Run(m, resourceguard.Budget{HeapBytes: 256 << 20, Goroutines: 16}))
}
