package promptrender_test

import (
	"os"
	"testing"

	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
)

// Generated worktree cases run the pinned Git.
func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}
