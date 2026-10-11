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
		name     string
		manifest bool
	}{
		{name: "missing manifest"},
		{name: "complete corpus", manifest: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			paths := contractcheck.InstallArtifactPaths(t, root, t.TempDir())
			calls := filepath.Join(root, "candidate-calls")
			files := map[string]string{
				"scripts/upgrade-fixture-files.py": "import sys\nwith open(" + pythonString(calls) + ", 'a') as log:\n    log.write(' '.join(sys.argv[1:]) + '\\n')\n",
				"tools/bun":                        "#!/bin/sh\nexit 0\n",
			}
			for _, rel := range preflightInputs {
				files[rel] = contractcheck.ReadRepoFile(t, repo, rel)
			}
			corpus := filepath.Join(root, "lycaon", "testdata", "upgrade-corpus", version)
			if tc.manifest {
				files[filepath.Join("lycaon", "testdata", "upgrade-corpus", version, "MANIFEST.json")] = "{}\n"
			}
			for rel, body := range files {
				absolute := filepath.Join(root, rel)
				contractcheck.FailErr(t, "create preflight fixture directory", os.MkdirAll(filepath.Dir(absolute), 0o700))
				contractcheck.FailErr(t, "write preflight fixture", os.WriteFile(absolute, []byte(body), 0o700))
			}
			contractcheck.FailErr(t, "create corpus", os.MkdirAll(corpus, 0o700))
			// Archive checks read the captured module and release refs; only corpus inputs are synthetic.
			for _, rel := range []string{".git", "third_party", "lycaon/cmd", "lycaon/internal", "lycaon/pkg", "lycaon/config"} {
				contractcheck.FailErr(t, "link preflight archive dependency "+rel,
					os.Symlink(filepath.Join(repo, rel), filepath.Join(root, rel)))
			}


			cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts", "release-preflight.sh"), "--require-corpus")
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd.Env = append(cmd.Env, paths.Env()...)
			out, err := cmd.CombinedOutput()
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
			want := "candidate " + corpus + " --sidecar " + filepath.Join(paths.Build, "lycaon-dev") + " --version " + version + "\n"
			if string(raw) != want {
				t.Fatalf("complete corpus verification = %q, want %q\n%s", raw, want, out)
			}
		})
	}
}

func pythonString(value string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), "'", `\'`) + "'"
}
