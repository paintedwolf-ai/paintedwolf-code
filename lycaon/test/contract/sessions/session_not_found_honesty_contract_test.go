package contract

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// sessionNotFoundHelpers are the two functions allowed to answer "this session is
// not there". Both read store.ErrSessionNotFound and send everything else to
// writeInternalError.
var sessionNotFoundHelpers = []string{"Session", "SessionExists"}

// TestSessionNotFoundNeverSwallowsStoreFailure: a store failure is not an absent
// session. A 404 tells Den the chat was deleted, so handlers write
// session_not_found only inside an errors.Is branch or through
// sessionNotFoundHelpers.
func TestSessionNotFoundNeverSwallowsStoreFailure(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "api")
	var findings []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		if filepath.ToSlash(name) == "requestscope/session.go" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			if !strings.Contains(line, `"session_not_found"`) && !strings.Contains(line, "ApiErrorCodeSessionNotFound") {
				continue
			}
			// Classified when the enclosing branch tested the store's own
			// sentinel. Look back a few lines for that test; both the
			// `if errors.Is(...)` and `case errors.Is(...)` shapes qualify.
			classified := false
			for back := i; back >= 0 && back > i-4; back-- {
				if strings.Contains(lines[back], "store.ErrSessionNotFound") {
					classified = true
					break
				}
			}
			if !classified {
				findings = append(findings, name+":"+strconv.Itoa(i+1)+" "+strings.TrimSpace(line))
			}
		}

		return nil
	})
	contractcheck.FailErr(t, "walk API session lookup handlers", err)

	if len(findings) > 0 {
		t.Fatalf("session_not_found written without classifying the store error — "+
			"use requestscope.Session/SessionExists so an unreadable store is not reported "+
			"as a deleted chat:\n  %s", strings.Join(findings, "\n  "))
	}

	// And the helpers must still be the thing that classifies.
	helperSrc := contractcheck.ReadRepoFile(t, root, "lycaon/internal/api/requestscope/session.go")
	if !strings.Contains(helperSrc, "store.ErrSessionNotFound") {
		t.Fatal("requestscope/session.go no longer reads store.ErrSessionNotFound")
	}
	if !strings.Contains(helperSrc, "responses.InternalError") {
		t.Fatal("requestscope/session.go no longer routes a store failure to responses.InternalError")
	}
	for _, helper := range sessionNotFoundHelpers {
		if !strings.Contains(helperSrc, "func "+helper+"(") {
			t.Fatalf("requestscope/session.go no longer defines %s", helper)
		}
	}
}
