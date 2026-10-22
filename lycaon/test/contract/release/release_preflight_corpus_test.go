package contract

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// preflightInputs are the files release-preflight.sh reads before its corpus check.
var preflightInputs = []string{
	"VERSION", "RELEASE_BUILD", "CHANGELOG.md",

	"lycaon-den/package.json", "lycaon-den/src-tauri/tauri.conf.json",
	"lycaon-den/src-tauri/Cargo.toml", "lycaon-den/src-tauri/Cargo.lock",
	"packaging/update-keys.json", "packaging/release-platforms.json",
	"packaging/homebrew/painted-wolf-code.rb.tmpl",
	"scripts/release-preflight.sh", "scripts/release-metadata.py",
	"scripts/release_semver.py", "scripts/update_keys.py",
}

func TestReleasePreflightVerifiesOnlyACompleteCorpus(t *testing.T) {
	repo := contractcheck.RepoRoot(t)
	version := strings.TrimSpace(contractcheck.ReadRepoFile(t, repo, "VERSION"))
	for _, tc := range []struct {
		name               string
		manifest           bool
		engineOverride     bool
		controller         bool
		compatibilityFails bool
	}{
		{name: "compatibility failure blocks corpus", manifest: true, compatibilityFails: true},
		{name: "controller compatibility failure blocks corpus", manifest: true, controller: true, compatibilityFails: true},
		{name: "missing manifest", engineOverride: true},
		{name: "complete corpus", manifest: true, engineOverride: true},
		{name: "complete corpus with checkout engine", manifest: true},
		{name: "controller validates selected corpus without building it", manifest: true, engineOverride: true, controller: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			contractcheck.FailErr(t, "create preflight module directory", os.MkdirAll(filepath.Join(root, "lycaon"), 0o700))
			paths := contractcheck.InstallArtifactPaths(t, root, t.TempDir())
			calls := filepath.Join(root, "candidate-calls")
			files := map[string]string{
				"task": `#!/bin/sh
[ "$*" = "build:lycaon-dev" ] || exit 2
printf built > "$PREFLIGHT_BUILD_CALLS"
`,
				"scripts/upgrade-fixture-files.py": "import sys\nwith open(" + pythonString(calls) + ", 'a') as log:\n    log.write(' '.join(sys.argv[1:]) + '\\n')\n",
				"tools/bun":                        "#!/bin/sh\nexit 0\n",
			}
			files["tools/go"] = `#!/bin/sh
printf '%s\n' "$PWD" "$LYCAON_ENGINE_ROOT" "$@" > "$PREFLIGHT_COMPATIBILITY_CALLS"
exit "${PREFLIGHT_COMPATIBILITY_EXIT:-0}"
`
			candidate := root
			if tc.controller {
				candidate = t.TempDir()
			}
			for _, rel := range preflightInputs {
				files[rel] = contractcheck.ReadRepoFile(t, repo, rel)
			}
			for rel, body := range files {
				absolute := filepath.Join(root, rel)
				contractcheck.FailErr(t, "create preflight fixture directory", os.MkdirAll(filepath.Dir(absolute), 0o700))
				contractcheck.FailErr(t, "write preflight fixture", os.WriteFile(absolute, []byte(body), 0o700))
			}
			corpus := filepath.Join(candidate, "lycaon", "testdata", "upgrade-corpus", version)
			contractcheck.FailErr(t, "create corpus", os.MkdirAll(corpus, 0o700))
			if tc.manifest {
				contractcheck.FailErr(t, "write corpus manifest", os.WriteFile(filepath.Join(corpus, "MANIFEST.json"), []byte("{}\n"), 0o700))
			}
			if tc.controller {
				contractcheck.FailErr(t, "write candidate VERSION", os.WriteFile(filepath.Join(candidate, "VERSION"), []byte(version+"\n"), 0o700))
			}
			args := []string{filepath.Join(root, "scripts", "release-preflight.sh"), "--require-corpus"}
			if tc.controller {
				args = []string{args[0], "--compatibility-only", "--source-root", candidate, "--baseline-ref", "v1.0.1", "--published-versions", `["1.0.1"]`}
			}
			cmd := exec.CommandContext(t.Context(), "bash", args...)
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd.Env = append(cmd.Env, paths.Env()...)
			builds := filepath.Join(root, "build-calls")
			compatibilityCalls := filepath.Join(root, "compatibility-calls")
			compatibilityExit := 0
			if tc.compatibilityFails {
				compatibilityExit = 7
			}
			cmd.Env = append(cmd.Env, "PREFLIGHT_BUILD_CALLS="+builds, "BASELINE_SHA=", "PREFLIGHT_COMPATIBILITY_CALLS="+compatibilityCalls, fmt.Sprintf("PREFLIGHT_COMPATIBILITY_EXIT=%d", compatibilityExit))
			// Exercise both an explicit engine root and the staged checkout default.
			engineRoot := ""
			if tc.engineOverride {
				engineRoot = filepath.Join(repo, "lycaon-den", "src-tauri", "engine-root")
			}
			cmd.Env = append(cmd.Env, "LYCAON_ENGINE_ROOT="+engineRoot)
			out, err := cmd.CombinedOutput()
			wantEngine := engineRoot
			if wantEngine == "" {
				wantEngine = filepath.Join(root, "lycaon-den/src-tauri/engine-root")
			}
			wantCompatibility := []string{filepath.Join(root, "lycaon"), wantEngine, "run", "./cmd/release-contracts", "--check-all", "--baseline", ""}
			if tc.controller {
				wantCompatibility = append(wantCompatibility[:len(wantCompatibility)-1], "v1.0.1", "--repo-root", candidate, "--published-versions", `["1.0.1"]`)
			}
			compatibility, compatibilityErr := os.ReadFile(compatibilityCalls)
			contractcheck.FailErr(t, "read compatibility calls", compatibilityErr)
			if string(compatibility) != strings.Join(wantCompatibility, "\n")+"\n" {
				t.Fatalf("compatibility command = %q, want %q", compatibility, wantCompatibility)
			}
			_, buildErr := os.Stat(builds)
			if tc.controller && !os.IsNotExist(buildErr) {
				t.Fatal("controller rebuilt the selected engine")
			}
			if !tc.controller {
				contractcheck.FailErr(t, "normal preflight builds its engine", buildErr)
			}
			raw, readErr := os.ReadFile(calls)
			if readErr != nil && !os.IsNotExist(readErr) {
				contractcheck.FailErr(t, "read candidate calls", readErr)
			}
			if tc.compatibilityFails {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 || len(raw) != 0 {
					t.Fatalf("compatibility failure lost: err=%v calls=%q\n%s", err, raw, out)
				}
				return
			}
			if !tc.manifest {
				if err == nil || !strings.Contains(string(out), "MANIFEST.json missing") || len(raw) != 0 {
					t.Fatalf("missing manifest passed or was verified: err=%v calls=%q\n%s", err, raw, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("complete corpus preflight failed: %v\n%s", err, out)
			}
			want := "candidate " + corpus + " --sidecar " + filepath.Join(paths.Build, "lycaon-dev") + " --version " + version + " --inventory-root " + filepath.Dir(corpus) + "\n"
			if tc.controller {
				want = "candidate-static " + corpus + " --schema-lock " + filepath.Join(candidate, "lycaon/internal/db/schema_version_lock.json") + " --version " + version + " --inventory-root " + filepath.Dir(corpus) + " --published-versions [\"1.0.1\"]\n"
			}
			if string(raw) != want {
				t.Fatalf("complete corpus verification = %q, want %q\n%s", raw, want, out)
			}
		})
	}
}

func pythonString(value string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), "'", `\'`) + "'"
}

func TestReleaseCompatibility(t *testing.T) {
	t.Parallel()
	repo := contractcheck.RepoRoot(t)
	cmd := exec.CommandContext(t.Context(), "go", "run", "./cmd/release-contracts", "--check-all")
	cmd.Dir = filepath.Join(repo, "lycaon")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("release compatibility failed: %v\n%s", err, out)
	}
}
