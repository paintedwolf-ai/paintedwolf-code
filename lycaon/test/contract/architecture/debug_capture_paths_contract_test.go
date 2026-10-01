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

// debugCapturePathDefinitionRoots is where a capture filename may appear literally:
// debugpaths declares them, so it is the one place they are spelled out.
var debugCapturePathDefinitionRoots = []string{
	filepath.Join("internal", "debugpaths"),
}

func TestDebugCaptureFilenamesLiveOnlyInDebugPaths(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	codeRoot := filepath.Join(root, "lycaon")

	var violations []string
	scanned := 0
	err := contractcheck.WalkFiles(codeRoot, map[string]struct{}{".go": {}}, true, func(path string, data []byte) error {
		for _, definitionRoot := range debugCapturePathDefinitionRoots {
			if strings.Contains(path, definitionRoot) {
				return nil
			}
		}
		scanned++
		text := string(data)
		for _, f := range debugpaths.Files() {
			if strings.Contains(text, `"`+f.Name+`"`) {
				violations = append(violations, path+": hardcodes "+f.Name)
			}
			if f.FileEnv != "" && strings.Contains(text, `"`+f.FileEnv+`"`) {
				violations = append(violations, path+": hardcodes "+f.FileEnv)
			}
			if f.EnableEnv != "" && strings.Contains(text, `"`+f.EnableEnv+`"`) {
				violations = append(violations, path+": hardcodes "+f.EnableEnv)
			}
		}
		for _, env := range []string{debugpaths.FullDebugEnv, debugpaths.SessionDirEnv} {
			if strings.Contains(text, `"`+env+`"`) {
				violations = append(violations, path+": hardcodes "+env)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk Go for capture filename literals", err)
	if scanned == 0 {
		t.Fatal("scan visited no production Go files — this guard is inert")
	}
	if len(debugpaths.Files()) == 0 {
		t.Fatal("capture catalog is empty — this guard is inert")
	}
	contractcheck.FailViolations(t, "production Go names a debug capture file or redirect variable directly (use internal/debugpaths)", violations)
}

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
