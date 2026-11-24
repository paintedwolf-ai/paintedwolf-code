//go:build darwin || linux

package native

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"golang.org/x/sys/unix"
)

func TestInspectSourceRejectsSpecialGitMetadata(t *testing.T) {
	dir := t.TempDir()
	writeGitInspectionFixture(t, dir, "HEAD", "ref: refs/heads/review")
	testutil.FailErr(t, "make commondir FIFO", unix.Mkfifo(filepath.Join(dir, ".git", "commondir"), 0o600))

	tool := &InspectSourceTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.inspectGitHead(t.Context(), provenanceCtx(dir, nil), ".")
	if !errors.Is(err, projectpaths.ErrNotRegular) {
		t.Fatalf("named pipe was not refused as metadata: %v", err)
	}
}
