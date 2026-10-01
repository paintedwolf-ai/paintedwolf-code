// Package gittest provides hermetic Git repository fixtures.
package gittest

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
)

// Init initializes a repository in dir.
func Init(t testing.TB, dir string) {
	t.Helper()
	Run(t, dir, "init")
}

// InitCommit initializes a repository and commits its current contents.
func InitCommit(t testing.TB, dir, message string) {
	t.Helper()
	Init(t, dir)
	CommitAll(t, dir, message)
}

// CommitAll stages every change and creates a commit.
func CommitAll(t testing.TB, dir, message string) {
	t.Helper()
	Run(t, dir, "add", "-A")
	Run(t, dir, "commit", "-m", message)
}

// CommitAt creates a commit with a fixed timestamp.
func CommitAt(t testing.TB, dir, message string, when time.Time) {
	t.Helper()
	run(t, dir, &when, "commit", "-m", message)
}

// Run executes Git with a stable fixture identity.
func Run(t testing.TB, dir string, args ...string) string {
	t.Helper()
	return run(t, dir, nil, args...)
}

func run(t testing.TB, dir string, when *time.Time, args ...string) string {
	t.Helper()
	identity := &gitexec.Identity{
		Name:  "Painted Wolf test",
		Email: "test@paintedwolf.dev",
	}
	if when != nil {
		identity.Timestamp = *when
	}
	out, code, err := gitexec.Run(t.Context(), dir, args, gitexec.Opts{
		Profile:  gitexec.ProfileHermetic,
		Identity: identity,
	})
	if err != nil || code != 0 {
		t.Fatalf("git %v in %s: exit %d: %v: %s", args, dir, code, err, out)
	}
	return string(out)
}
