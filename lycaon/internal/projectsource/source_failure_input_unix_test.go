//go:build !windows

package projectsource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFailedWriteReleasesObservationWithoutRestart(t *testing.T) {
	service, p, row := pendingObservationWrite(t, sourceMutationPrepared)
	path := filepath.Join(p.Roots[0].Path, "a.txt")
	testutil.FailErr(t, "restore prepared input", os.WriteFile(path, []byte("before"), 0600))
	testutil.FailErr(t, "deny parent writes", os.Chmod(p.Roots[0].Path, 0500))
	_, err := service.resume(t.Context(), row)
	testutil.FailErr(t, "restore parent mode", os.Chmod(p.Roots[0].Path, 0700))
	if err == nil || row.Status != sourceMutationFailed {
		t.Fatalf("failed effect retained observation scope: %s %v", row.Status, err)
	}
	testutil.FailErr(t, "external later write", os.WriteFile(path, []byte("outside later"), 0600))
	if count := observeMutationPaths(t, service, p, "a.txt"); count != 1 {
		t.Fatalf("failed effect suppressed external write: %d", count)
	}
}
