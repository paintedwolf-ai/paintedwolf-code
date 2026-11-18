package contract

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/debugpaths"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Debug capture layout — config root, debug/sessions/latest tree, capture
// filenames, LYCAON_*_FILE redirects — is defined by internal/debugpaths.
// Guards below keep writers, the log viewer, clear registry, and shell scripts
// from spelling those paths a second time.

// The session tree the scripts create is the tree the viewer reads.
func TestDebugCaptureScriptTreeMatchesLayout(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	body := contractcheck.ReadRepoFile(t, root, filepath.Join("scripts", "debug-session.sh"))

	for _, want := range []string{
		`DEBUG_ROOT="${CONFIG_DIR}/` + filepath.Base(debugpaths.DebugRoot()) + `"`,
		`SESSIONS_DIR="${DEBUG_ROOT}/` + filepath.Base(debugpaths.SessionsRoot()) + `"`,
		`"${DEBUG_ROOT}/` + filepath.Base(debugpaths.LatestLink()) + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scripts/debug-session.sh missing %s", want)
		}
	}
}
