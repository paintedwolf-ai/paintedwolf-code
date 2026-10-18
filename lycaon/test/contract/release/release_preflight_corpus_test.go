package contract

import (
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
	"lycaon/go.mod", "lycaon/go.sum",
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
		name           string
		manifest       bool
		engineOverride bool
		controller     bool
	}{
		{name: "missing manifest", engineOverride: true},
		{name: "complete corpus", manifest: true, engineOverride: true},
		{name: "complete corpus with checkout engine", manifest: true},
		{name: "controller validates selected corpus without building it", manifest: true, engineOverride: true, controller: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
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
				contractcheck.FailErr(t, "link candidate git", os.Symlink(filepath.Join(repo, ".git"), filepath.Join(candidate, ".git")))
			}
			// Candidate inputs stay inside the source boundary.
			for _, rel := range []string{"schemas", "lycaon/internal", "lycaon/config"} {
				contractcheck.FailErr(t, "copy preflight candidate "+rel, os.CopyFS(filepath.Join(candidate, rel), os.DirFS(filepath.Join(repo, rel))))
			}
			controllerLinks := []string{".git", "third_party", "lycaon/cmd", "lycaon/pkg", "lycaon-den/src-tauri/engine-root"}
			if tc.controller {
				controllerLinks = append(controllerLinks, "lycaon/internal", "lycaon/config")
			}
			for _, rel := range controllerLinks {
				contractcheck.FailErr(t, "link preflight archive dependency "+rel,
					os.Symlink(filepath.Join(repo, rel), filepath.Join(root, rel)))
			}
			args := []string{filepath.Join(root, "scripts", "release-preflight.sh"), "--require-corpus"}
			if tc.controller {
				args = []string{args[0], "--compatibility-only", "--source-root", candidate, "--baseline-ref", "v1.0.1", "--published-versions", `["1.0.1"]`}
			}
			cmd := exec.CommandContext(t.Context(), "bash", args...)
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd.Env = append(cmd.Env, paths.Env()...)
			builds := filepath.Join(root, "build-calls")
			cmd.Env = append(cmd.Env, "PREFLIGHT_BUILD_CALLS="+builds)
			// Exercise both an explicit engine root and the staged checkout default.
			engineRoot := ""
			if tc.engineOverride {
				engineRoot = filepath.Join(repo, "lycaon-den", "src-tauri", "engine-root")
			}
			cmd.Env = append(cmd.Env, "LYCAON_ENGINE_ROOT="+engineRoot)
			out, err := cmd.CombinedOutput()
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
			if !tc.manifest {
				if err == nil || !strings.Contains(string(out), "MANIFEST.json missing") || len(raw) != 0 {
					t.Fatalf("missing manifest passed or was verified: err=%v calls=%q\n%s", err, raw, out)
				}
				return
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
