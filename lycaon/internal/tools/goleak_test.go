package tools

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// TestMain checks suite goroutine cleanup.
func TestMain(m *testing.M) {
	testutil.VerifyNoLeaks(m, nil)
}
