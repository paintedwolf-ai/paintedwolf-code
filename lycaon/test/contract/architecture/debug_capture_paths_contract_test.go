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

// Capture scripts cannot import the Go catalog.
func TestDebugCaptureScriptsNameOnlyCatalogFiles(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	names := map[string]struct{}{}
	envs := map[string]struct{}{}
	for _, f := range debugpaths.Files() {
		names[f.Name] = struct{}{}
		if f.FileEnv != "" {
			envs[f.FileEnv] = struct{}{}
		}
	}

	var violations []string
	for _, rel := range []string{
		filepath.Join("scripts", "debug-session.sh"),
		filepath.Join("scripts", "debug-capture.sh"),
	} {
		body := contractcheck.ReadRepoFile(t, root, rel)
		for _, word := range strings.FieldsFunc(body, func(r rune) bool {
			return !(r == '.' || r == '-' || r == '_' || r == '/' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
		}) {
			switch {
			case strings.HasSuffix(word, ".jsonl") || strings.HasSuffix(word, ".log"):
				if _, ok := names[filepath.Base(word)]; !ok {
					violations = append(violations, rel+": writes "+word+", absent from the catalog")
				}
			case strings.HasPrefix(word, "LYCAON_") && strings.HasSuffix(word, "_FILE"):
				if _, ok := envs[word]; !ok {
					violations = append(violations, rel+": exports "+word+", absent from the catalog")
				}
			}
		}
	}
	contractcheck.FailViolations(t, "a capture script names a file or variable internal/debugpaths does not declare", violations)
}

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
