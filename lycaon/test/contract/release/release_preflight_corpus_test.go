package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// preflightInputs are the source files release-preflight.sh reads.
var preflightInputs = []string{
	"VERSION", "RELEASE_BUILD", "CHANGELOG.md",
	"lycaon-den/package.json", "lycaon-den/src-tauri/tauri.conf.json",
	"lycaon-den/src-tauri/Cargo.toml", "lycaon-den/src-tauri/Cargo.lock",
	"packaging/update-keys.json", "packaging/release-platforms.json",
	"packaging/homebrew/painted-wolf-code.rb.tmpl",
	"scripts/release-preflight.sh", "scripts/release-metadata.py",
	"scripts/release_semver.py", "scripts/update_keys.py",
	"scripts/release_distribution_contract/platforms.py",
}

// preflightFixture is a checkout holding the preflight inputs and recording doubles for every tool the script launches.
type preflightFixture struct {
	root, build, builds, compatibility, corpusCalls string
	env                                             []string
}

func newPreflightFixture(t *testing.T, replace map[string]string) preflightFixture {
	t.Helper()
	repo := contractcheck.RepoRoot(t)
	root := t.TempDir()
	contractcheck.FailErr(t, "create preflight module directory", os.MkdirAll(filepath.Join(root, "lycaon"), 0o700))
	paths := contractcheck.InstallArtifactPaths(t, root, t.TempDir())
	fixture := preflightFixture{root: root, build: paths.Build, builds: filepath.Join(root, "build-calls"),
		compatibility: filepath.Join(root, "compatibility-calls"), corpusCalls: filepath.Join(root, "candidate-calls")}
	files := map[string]string{
		"task": `#!/bin/sh
[ "$*" = "build:lycaon-dev" ] || exit 2
printf built > "$PREFLIGHT_BUILD_CALLS"
`,
		"scripts/upgrade-fixture-files.py": "import sys\nwith open(" + pythonString(fixture.corpusCalls) + ", 'a') as log:\n    log.write(' '.join(sys.argv[1:]) + '\\n')\n",
		"tools/bun":                        "#!/bin/sh\nprintf %s https://github.com/paintedwolf-ai/paintedwolf-code/issues\n",
		"tools/go": `#!/bin/sh
printf '%s\n' "$PWD" "$LYCAON_ENGINE_ROOT" "$@" > "$PREFLIGHT_COMPATIBILITY_CALLS"
exit "${PREFLIGHT_COMPATIBILITY_EXIT:-0}"
`,
	}
	for _, rel := range preflightInputs {
		files[rel] = contractcheck.ReadRepoFile(t, repo, rel)
	}
	for rel, body := range replace {
		files[rel] = body
	}
	for rel, body := range files {
		absolute := filepath.Join(root, rel)
		contractcheck.FailErr(t, "create preflight fixture directory", os.MkdirAll(filepath.Dir(absolute), 0o700))
		contractcheck.FailErr(t, "write preflight fixture", os.WriteFile(absolute, []byte(body), 0o700))
	}
	fixture.env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
	fixture.env = append(fixture.env, paths.Env()...)
	fixture.env = append(fixture.env, "PREFLIGHT_BUILD_CALLS="+fixture.builds, "PREFLIGHT_COMPATIBILITY_CALLS="+fixture.compatibility)
	return fixture
}

func (f preflightFixture) run(t *testing.T, env []string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "bash", append([]string{filepath.Join(f.root, "scripts", "release-preflight.sh")}, args...)...)
	cmd.Env = append(f.env, env...)
	return cmd.CombinedOutput()
}

func (f preflightFixture) recorded(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		contractcheck.FailErr(t, "read recorded calls", err)
	}
	return string(raw)
}

// Static preflight judges source metadata alone: a defect in any one input fails it, and no mode but the corpus check builds.
func TestReleasePreflightStaticChecksSourceWithoutBuilding(t *testing.T) {
	t.Parallel()
	repo := contractcheck.RepoRoot(t)
	version := strings.TrimSpace(contractcheck.ReadRepoFile(t, repo, "VERSION"))
	source := func(rel string) string { return contractcheck.ReadRepoFile(t, repo, rel) }
	var catalog map[string]any
	contractcheck.FailErr(t, "decode platform catalog", json.Unmarshal([]byte(source("packaging/release-platforms.json")), &catalog))
	catalog["platforms"] = append(catalog["platforms"].([]any), map[string]any{
		"updater_key": "plan9-x86_64", "runner": "ubuntu-24.04", "artifact_arch": "x86_64", "goos": "plan9", "goarch": "amd64",
		"publication": "candidate", "package_extensions": []string{"tar"}, "updater_extension": nil})
	unknownPlatform, err := json.Marshal(catalog)
	contractcheck.FailErr(t, "encode platform catalog", err)
	var manifest map[string]any
	contractcheck.FailErr(t, "decode Den package manifest", json.Unmarshal([]byte(source("lycaon-den/package.json")), &manifest))
	manifest["version"] = "0.0.1"
	wrongVersion, err := json.Marshal(manifest)
	contractcheck.FailErr(t, "encode Den package manifest", err)
	for _, tc := range []struct {
		name    string
		replace map[string]string
		passes  bool
	}{
		{name: "committed source", passes: true},
		{name: "wrong version projection", replace: map[string]string{"lycaon-den/package.json": string(wrongVersion)}},
		{name: "missing changelog section", replace: map[string]string{
			"CHANGELOG.md": strings.ReplaceAll(source("CHANGELOG.md"), "## ["+version+"]", "## [0.0.1]")}},
		{name: "unknown platform", replace: map[string]string{"packaging/release-platforms.json": string(unknownPlatform)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPreflightFixture(t, tc.replace)
			out, err := fixture.run(t, nil, "--static")
			if (err == nil) != tc.passes {
				t.Fatalf("static preflight passed=%v, want %v\n%s", err == nil, tc.passes, out)
			}
			if fixture.recorded(t, fixture.builds)+fixture.recorded(t, fixture.compatibility)+fixture.recorded(t, fixture.corpusCalls) != "" {
				t.Fatalf("static preflight built or launched another check\n%s", out)
			}
		})
	}
}

// Exactly one mode runs per invocation, so no caller gets a check it did not name.
func TestReleasePreflightRequiresOneMode(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"--static", "--require-corpus"}, {"--static", "--baseline-ref", "v1.0.1"}} {
		fixture := newPreflightFixture(t, nil)
		out, err := fixture.run(t, nil, args...)
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
			t.Fatalf("preflight %v: err=%v, want usage exit 2\n%s", args, err, out)
		}
		if fixture.recorded(t, fixture.builds) != "" {
			t.Fatalf("preflight %v built before refusing its arguments", args)
		}
	}
}

func TestReleasePreflightVerifiesOnlyACompleteCorpus(t *testing.T) {
	t.Parallel()
	repo := contractcheck.RepoRoot(t)
	version := strings.TrimSpace(contractcheck.ReadRepoFile(t, repo, "VERSION"))
	for _, tc := range []struct {
		name               string
		manifest           bool
		engineOverride     bool
		controller         bool
		compatibilityFails bool
	}{
		{name: "controller compatibility failure blocks corpus", manifest: true, controller: true, compatibilityFails: true},
		{name: "missing manifest"},
		{name: "complete corpus", manifest: true},
		{name: "controller validates selected corpus without building it", manifest: true, engineOverride: true, controller: true},
		{name: "controller with checkout engine", manifest: true, controller: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPreflightFixture(t, nil)
			root := fixture.root
			candidate := root
			if tc.controller {
				candidate = t.TempDir()
			}
			corpus := filepath.Join(candidate, "lycaon", "testdata", "upgrade-corpus", version)
			contractcheck.FailErr(t, "create corpus", os.MkdirAll(corpus, 0o700))
			if tc.manifest {
				contractcheck.FailErr(t, "write corpus manifest", os.WriteFile(filepath.Join(corpus, "MANIFEST.json"), []byte("{}\n"), 0o700))
			}
			args := []string{"--require-corpus"}
			if tc.controller {
				contractcheck.FailErr(t, "write candidate VERSION", os.WriteFile(filepath.Join(candidate, "VERSION"), []byte(version+"\n"), 0o700))
				args = []string{"--compatibility-only", "--source-root", candidate, "--baseline-ref", "v1.0.1", "--published-versions", `["1.0.1"]`}
			}
			compatibilityExit := 0
			if tc.compatibilityFails {
				compatibilityExit = 7
			}
			// Exercise both an explicit engine root and the staged checkout default.
			engineRoot := ""
			if tc.engineOverride {
				engineRoot = filepath.Join(repo, "lycaon-den", "src-tauri", "engine-root")
			}
			out, err := fixture.run(t, []string{fmt.Sprintf("PREFLIGHT_COMPATIBILITY_EXIT=%d", compatibilityExit), "LYCAON_ENGINE_ROOT=" + engineRoot}, args...)
			wantEngine := engineRoot
			if wantEngine == "" {
				wantEngine = filepath.Join(root, "lycaon-den/src-tauri/engine-root")
			}
			wantCompatibility := ""
			if tc.controller {
				wantCompatibility = strings.Join([]string{filepath.Join(root, "lycaon"), wantEngine, "run", "./cmd/release-contracts", "--check-all",
					"--baseline", "v1.0.1", "--repo-root", candidate, "--published-versions", `["1.0.1"]`}, "\n") + "\n"
			}
			if compatibility := fixture.recorded(t, fixture.compatibility); compatibility != wantCompatibility {
				t.Fatalf("compatibility command = %q, want %q", compatibility, wantCompatibility)
			}
			// Only a corpus that exists is worth an engine build.
			if built := fixture.recorded(t, fixture.builds) != ""; built != (tc.manifest && !tc.controller) {
				t.Fatalf("engine built=%v for controller=%v manifest=%v\n%s", built, tc.controller, tc.manifest, out)
			}
			raw := fixture.recorded(t, fixture.corpusCalls)
			if tc.compatibilityFails {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 || len(raw) != 0 {
					t.Fatalf("compatibility failure lost: err=%v calls=%q\n%s", err, raw, out)
				}
				return
			}
			if !tc.manifest {
				if err == nil || len(raw) != 0 {
					t.Fatalf("missing manifest passed or was verified: err=%v calls=%q\n%s", err, raw, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("complete corpus preflight failed: %v\n%s", err, out)
			}
			want := "candidate " + corpus + " --sidecar " + filepath.Join(fixture.build, "lycaon-dev") + " --version " + version + " --inventory-root " + filepath.Dir(corpus) + "\n"
			if tc.controller {
				want = "candidate-static " + corpus + " --schema-lock " + filepath.Join(candidate, "lycaon/internal/db/schema_version_lock.json") + " --version " + version + " --inventory-root " + filepath.Dir(corpus) + " --published-versions [\"1.0.1\"]\n"
			}
			if raw != want {
				t.Fatalf("complete corpus verification = %q, want %q\n%s", raw, want, out)
			}
		})
	}
}

func pythonString(value string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), "'", `\'`) + "'"
}

// stableReleaseTag names a published stable release; preview tags may be cut without publishing.
var stableReleaseTag = regexp.MustCompile(`^v([1-9][0-9]*\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))$`)

// publishedStableVersions lists the stable releases whose migration identities the checkout must keep.
func publishedStableVersions(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "-C", repo, "tag", "--list", "v*").Output()
	contractcheck.FailErr(t, "list release tags", err)
	versions := []string{}
	for _, tag := range strings.Fields(string(out)) {
		if match := stableReleaseTag.FindStringSubmatch(tag); match != nil {
			versions = append(versions, match[1])
		}
	}
	if len(versions) == 0 {
		t.Fatal("no published stable release tags; fetch tags before running release contracts")
	}
	raw, err := json.Marshal(versions)
	contractcheck.FailErr(t, "encode published versions", err)
	return string(raw)
}

func TestReleaseCompatibility(t *testing.T) {
	t.Parallel()
	repo := contractcheck.RepoRoot(t)
	cmd := exec.CommandContext(t.Context(), "go", "run", "./cmd/release-contracts", "--check-all",
		"--published-versions", publishedStableVersions(t, repo))
	cmd.Dir = filepath.Join(repo, "lycaon")
	cmd.Env = append(os.Environ(), "LYCAON_ENGINE_ROOT="+filepath.Join(repo, "lycaon-den/src-tauri/engine-root"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("release compatibility failed: %v\n%s", err, out)
	}
}
