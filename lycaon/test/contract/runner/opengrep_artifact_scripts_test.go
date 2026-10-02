package contract

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type scannerScriptFixture struct {
	root, tools, log, artifact string
	paths                      contractcheck.ArtifactPaths
}

func scannerScriptFile(t *testing.T, path, contents string) {
	t.Helper()
	contractcheck.FailErr(t, "create fixture directory", os.MkdirAll(filepath.Dir(path), 0o700))
	contractcheck.FailErr(t, "write script fixture", os.WriteFile(path, []byte(contents), 0o700))
}

func newScannerScriptFixture(t *testing.T) scannerScriptFixture {
	t.Helper()
	f := scannerScriptFixture{root: t.TempDir()}
	canonicalRoot, err := filepath.EvalSymlinks(f.root)
	contractcheck.FailErr(t, "resolve scratch directory", err)
	f.root = canonicalRoot
	f.tools = filepath.Join(f.root, "tools")
	f.log = filepath.Join(f.root, "calls")
	f.artifact = filepath.Join(f.root, "qualified artifact")
	f.paths = contractcheck.InstallArtifactPaths(t, f.root, filepath.Join(f.root, "user cache"))
	for _, script := range []string{"resolve-opengrep.sh", "select-opengrep-release.sh", "stage-bundled-opengrep.sh", "build-dev-engine.sh", "stage-engine.sh"} {
		scannerScriptFile(t, filepath.Join(f.root, "scripts", script), contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/"+script))
	}
	for _, file := range []string{"lycaon/go.mod", "lycaon/go.sum", "lycaon/main.go", "lycaon/config/pin.yaml", "VERSION", "lycaon/config/runtime/scanners/bundled-manifest.yaml", "schemas/example.json", "lycaon/internal/platformfloor/macos_floor.txt"} {
		scannerScriptFile(t, filepath.Join(f.root, file), "1.0.0\n")
	}
	scannerScriptFile(t, filepath.Join(f.root, "scripts/sign-dev-binary.sh"), "#!/bin/bash\nexit 0\n")
	scannerScriptFile(t, filepath.Join(f.root, "scripts/build-decide.sh"), "#!/bin/bash\nmkdir -p \"$(dirname \"$1\")\"\n: > \"$1\"\n")
	scannerScriptFile(t, filepath.Join(f.root, "scripts/build-document-core.sh"), "#!/bin/bash\nmkdir -p \"$(dirname \"$2\")\"\n: > \"$2\"\nprintf '%s\\n' \"$2\"\n")
	scannerScriptFile(t, filepath.Join(f.root, "scripts/stage-decide-heads.sh"), "#!/bin/bash\nexit 0\n")
	scannerScriptFile(t, filepath.Join(f.tools, "rustc"), "#!/bin/bash\nprintf 'rustc\\n' >> \"$SCANNER_TEST_LOG\"\nprintf 'aarch64-apple-darwin\\n'\n")

	scannerScriptFile(t, filepath.Join(f.tools, "go"), scannerGoStub)
	return f
}

const scannerGoStub = `#!/bin/bash
set -euo pipefail
printf 'go|candidate=%s' "${LYCAON_OPENGREP_CANDIDATE:-}" >> "$SCANNER_TEST_LOG"
printf '|%s' "$@" >> "$SCANNER_TEST_LOG"
printf '\n' >> "$SCANNER_TEST_LOG"
if [[ "$1" == env ]]; then printf '/tmp/scanner-test-gopath\n'; exit 0; fi
if [[ "$1" == build ]]; then
 output=""
 while [[ $# -gt 0 ]]; do if [[ "$1" == -o ]]; then output="$2"; shift; fi; shift; done
 [[ "$output" == "$SCANNER_TEST_ROOT/"* ]] || exit 90
 mkdir -p "$(dirname "$output")"
 cat > "$output" <<'SIDECAR'
#!/bin/bash
set -euo pipefail
printf 'sidecar|candidate=%s' "${LYCAON_OPENGREP_CANDIDATE:-}" >> "$SCANNER_TEST_LOG"
printf '|%s' "$@" >> "$SCANNER_TEST_LOG"
printf '\n' >> "$SCANNER_TEST_LOG"
root=""
while [[ $# -gt 0 ]]; do if [[ "$1" == --root ]]; then root="$2"; shift; fi; shift; done
[[ -z "${LYCAON_OPENGREP_CANDIDATE:-}" ]] || exit 92
[[ -f "$root/bundled/proof" && "$(cat "$root/bundled/proof")" == verified ]] || exit 1
printf '%s/bundled/verified/opengrep\n' "$root"
SIDECAR
 chmod +x "$output"
 exit 0
fi
mode=""; root=""; executable=""; dir_only=0
while [[ $# -gt 0 ]]; do
 case "$1" in -mode) mode="$2"; shift;; -root) root="$2"; shift;; -executable) executable="$2"; shift;; -dir-only) dir_only=1;; esac
 shift
done
if [[ "$mode" == select ]]; then printf 'selected release\n'; exit 0; fi
if [[ "$mode" == fetch ]]; then
 if [[ "${SCANNER_TEST_FAIL:-}" == fetch ]]; then exit 7; fi
 printf '%s\n' "$SCANNER_TEST_ARTIFACT"; exit 0
fi
if [[ "$mode" == identity ]]; then
 if [[ "${SCANNER_TEST_FAIL:-}" == identity ]]; then exit 8; fi
 printf 'qualified-identity\n'; exit 0
fi
if [[ -n "$executable" ]]; then printf '%s\n' "$executable"; exit 0; fi
[[ "$root" == "$SCANNER_TEST_ROOT/"* ]] || exit 91
mkdir -p "$root/bundled"
printf 'verified\n' > "$root/bundled/proof"
if [[ "$dir_only" == 1 ]]; then printf '%s/bundled/verified\n' "$root"; else printf '%s/bundled/verified/opengrep\n' "$root"; fi
`

func (f scannerScriptFixture) env(extra ...string) []string {
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "PATH", "OPENGREP", "OPENGREP_SHA256", "LYCAON_OPENGREP_CANDIDATE", "OPENGREP_STAGE_DIR", "OPENGREP_CACHE_DIR", "GOFLAGS", "TASK_TEMP_DIR":
			continue
		}
		result = append(result, entry)
	}
	result = append(result, "PATH="+f.tools+string(os.PathListSeparator)+os.Getenv("PATH"), "SCANNER_TEST_ROOT="+f.root, "SCANNER_TEST_LOG="+f.log, "SCANNER_TEST_ARTIFACT="+f.artifact, "TASK_TEMP_DIR="+filepath.Join(f.root, ".task"))
	result = append(result, f.paths.Env()...)
	return append(result, extra...)
}

func (f scannerScriptFixture) run(t *testing.T, script string, args []string, extra ...string) (string, error) {
	t.Helper()
	shell := "bash"
	if runtime.GOOS == "darwin" {
		shell = "/bin/bash"
	}
	cmd := exec.CommandContext(t.Context(), shell, append([]string{filepath.Join(f.root, "scripts", script)}, args...)...)
	cmd.Dir = f.root
	cmd.Env = f.env(extra...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
func (f scannerScriptFixture) calls(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return ""
	}
	contractcheck.FailErr(t, "read wrapper calls", err)
	return string(raw)
}

func TestOpenGrepResolverSeparatesReleaseAndExperimentalSelection(t *testing.T) {
	cases := []struct {
		name      string
		args, env []string
		fetch     bool
		want      string
		reject    bool
	}{
		{name: "normal", args: []string{"--dir-only"}, fetch: true, want: "|-mode|resolve|-root|"},
		{name: "identity", args: []string{"--identity-only"}, fetch: true, want: "go|candidate=|run|./cmd/opengrep-artifact|-mode|identity|-artifact-directory|"},
		{name: "artifact directory", args: []string{"--artifact-dir-only"}, fetch: true},
		{name: "candidate execution", args: []string{"--dir-only"}, env: []string{"LYCAON_OPENGREP_CANDIDATE=/explicit/candidate"}, want: "go|candidate=/explicit/candidate|run|"},
		{name: "candidate does not become shipping authority", args: []string{"--identity-only"}, env: []string{"LYCAON_OPENGREP_CANDIDATE=/explicit/candidate"}, fetch: true, want: "go|candidate=|run|./cmd/opengrep-artifact|-mode|identity"},
		{name: "explicit evaluation", env: []string{"OPENGREP=/explicit/opengrep", "OPENGREP_SHA256=explicit-digest"}, want: "|-executable|/explicit/opengrep|-sha256|explicit-digest"},
		{name: "override cannot identify build", args: []string{"--identity-only"}, env: []string{"OPENGREP=/explicit/opengrep", "OPENGREP_SHA256=explicit-digest"}, reject: true},
		{name: "override requires digest", env: []string{"OPENGREP=/explicit/opengrep"}, reject: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newScannerScriptFixture(t)
			output, err := f.run(t, "resolve-opengrep.sh", tc.args, tc.env...)
			if (err != nil) != tc.reject {
				t.Fatalf("resolver error=%v, reject=%v\n%s", err, tc.reject, output)
			}
			calls := f.calls(t)
			if strings.Contains(calls, "|-mode|fetch|") != tc.fetch {
				t.Fatalf("wrong release fetch selection:\n%s", calls)
			}
			if tc.fetch && tc.name != "artifact directory" && !strings.Contains(calls, "|-artifact-directory|"+f.artifact) {
				t.Fatalf("artifact directory was not passed as one exact argument:\n%s", calls)
			}
			if tc.want != "" && !strings.Contains(calls, tc.want) {
				t.Fatalf("missing exact argument propagation %q:\n%s", tc.want, calls)
			}
			if tc.name == "artifact directory" && (strings.TrimSpace(output) != f.artifact || strings.Count(calls, "go|") != 1) {
				t.Fatalf("artifact directory mode changed bytes or resolved twice: %q\n%s", output, calls)
			}
			if tc.reject && calls != "" {
				t.Fatalf("rejected authority reached a tool:\n%s", calls)
			}
		})
	}
}

func TestOpenGrepStageFailurePreservesExistingPayload(t *testing.T) {
	for _, phase := range []string{"fetch", "identity"} {
		t.Run(phase, func(t *testing.T) {
			f := newScannerScriptFixture(t)
			sentinel := filepath.Join(f.root, "lycaon-den/src-tauri/engine-root/sentinel")
			scannerScriptFile(t, sentinel, "preserve")
			output, err := f.run(t, "stage-engine.sh", []string{"--release"}, "SCANNER_TEST_FAIL="+phase)
			wantedStatus := 7
			if phase == "identity" {
				wantedStatus = 8
			}
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != wantedStatus {
				t.Fatalf("qualification did not reach failing %s phase: %v\n%s", phase, err, output)
			}
			raw, readErr := os.ReadFile(sentinel)
			contractcheck.FailErr(t, "existing stage survived failed qualification", readErr)
			if string(raw) != "preserve" {
				t.Fatal("failed build changed prior payload")
			}
			if _, err := os.Stat(filepath.Join(f.root, "lycaon-den/src-tauri/binaries")); !os.IsNotExist(err) {
				t.Fatalf("qualification failure mutated binary staging: %v", err)
			}
		})
	}
}

func TestOpenGrepVerificationUsesPackagedSidecarAuthority(t *testing.T) {
	f := newScannerScriptFixture(t)
	sidecar := filepath.Join(f.root, "packaged sidecar")
	scannerScriptFile(t, sidecar, "#!/bin/bash\nprintf 'native|%s\\n' \"$*\" >> \"$SCANNER_TEST_LOG\"\nprintf '/verified/executable\\n'\n")
	output, err := f.run(t, "stage-bundled-opengrep.sh", []string{"--verify", "relative engine root", "aarch64-apple-darwin", sidecar})
	contractcheck.FailErr(t, "verify through packaged sidecar", err)
	if strings.TrimSpace(output) != "/verified/executable" {
		t.Fatalf("verification stdout changed: %q", output)
	}
	calls := f.calls(t)
	if strings.Contains(calls, "go|") || !strings.Contains(calls, "native|scan engines verify-bundled --root "+filepath.Join(f.root, "relative engine root")) {
		t.Fatalf("verification regenerated build authority:\n%s", calls)
	}
}

func TestOpenGrepDevBuildEmbedsOneResolvedIdentity(t *testing.T) {
	f := newScannerScriptFixture(t)
	output, err := f.run(t, "build-dev-engine.sh", nil, "LYCAON_OPENGREP_CANDIDATE=/explicit/candidate")
	if err != nil {
		t.Fatalf("development build: %v\n%s", err, output)
	}
	calls := f.calls(t)
	if strings.Count(calls, "|-mode|fetch|") != 1 || strings.Count(calls, "buildIdentityBase64=qualified-identity") != 2 || strings.Contains(calls, "rustc\n") || !strings.Contains(calls, "go|candidate=|run|./cmd/opengrep-artifact|-mode|stage|-artifact-directory|"+f.artifact) {
		t.Fatalf("development identity/stage diverged:\n%s", calls)
	}
	for _, name := range []string{"lycaon-dev", "pw-logs"} {
		if _, err := os.Stat(filepath.Join(f.paths.Build, name)); err != nil {
			t.Fatalf("%s did not land in the checkout build directory: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(f.paths.Bin, name)); !os.IsNotExist(err) {
			t.Fatalf("%s landed in the user-wide tool directory: %v", name, err)
		}
	}
}

func TestOpenGrepCandidateStageIsCheckoutScoped(t *testing.T) {
	f := newScannerScriptFixture(t)
	output, err := f.run(t, "resolve-opengrep.sh", []string{"--dir-only"}, "LYCAON_OPENGREP_CANDIDATE=/explicit/candidate")
	if err != nil {
		t.Fatalf("resolve candidate: %v\n%s", err, output)
	}
	if want := "|-mode|resolve|-root|" + filepath.Join(f.paths.Build, "opengrep-bundle") + "|"; !strings.Contains(f.calls(t), want) {
		t.Fatalf("candidate stage left the checkout build directory; want %q:\n%s", want, f.calls(t))
	}
}

func TestOpenGrepDevTaskRepairsMissingStageAfterCacheHit(t *testing.T) {
	f := newScannerScriptFixture(t)
	scannerScriptFile(t, filepath.Join(f.root, "Taskfile.yml"), contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "Taskfile.yml"))
	// The wrapper runs the pinned Task from the fixture's tool directory.
	resolved, err := exec.CommandContext(t.Context(), "python3", filepath.Join(contractcheck.RepoRoot(t), "scripts", "artifact_paths.py"), "bin", contractcheck.RepoRoot(t)).Output()
	contractcheck.FailErr(t, "resolve the pinned tool directory", err)
	pinnedTask, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(resolved)), "task"))
	contractcheck.FailErr(t, "read the pinned Task binary", err)
	scannerScriptFile(t, filepath.Join(f.paths.Bin, "task"), string(pinnedTask))
	run := func() {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), filepath.Join(contractcheck.RepoRoot(t), "task"), "--dir", f.root, "--taskfile", filepath.Join(f.root, "Taskfile.yml"), "build:lycaon-dev")
		cmd.Env = f.env("LYCAON_OPENGREP_CANDIDATE=/explicit/candidate")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("development Task: %v\n%s", err, output)
		}
	}
	run()
	first := strings.Count(f.calls(t), "buildIdentityBase64=qualified-identity")
	if first != 2 {
		t.Fatalf("first build count=%d:\n%s", first, f.calls(t))
	}
	run()
	if count := strings.Count(f.calls(t), "buildIdentityBase64=qualified-identity"); count != first {
		t.Fatalf("unchanged verified build did not cache: %d", count)
	}
	contractcheck.FailErr(t, "remove staged artifact proof", os.Remove(filepath.Join(f.root, "lycaon-den/src-tauri/engine-root/bundled/proof")))
	run()
	if count := strings.Count(f.calls(t), "buildIdentityBase64=qualified-identity"); count != first+2 {
		t.Fatalf("missing payload did not rebuild: %d\n%s", count, f.calls(t))
	}
	scannerScriptFile(t, filepath.Join(f.root, "lycaon-den/src-tauri/engine-root/bundled/proof"), "changed")
	run()
	if count := strings.Count(f.calls(t), "buildIdentityBase64=qualified-identity"); count != first+4 {
		t.Fatalf("changed payload did not rebuild: %d\n%s", count, f.calls(t))
	}
	scannerScriptFile(t, filepath.Join(f.root, "lycaon/config/runtime/scanners/bundled-manifest.yaml"), "selected release changed\n")
	run()
	if count := strings.Count(f.calls(t), "buildIdentityBase64=qualified-identity"); count != first+6 {
		t.Fatalf("changed release pin did not rebuild: %d\n%s", count, f.calls(t))
	}
	if strings.Contains(f.calls(t), "sidecar|candidate=/explicit/candidate") {
		t.Fatal("development cache verification used experimental runtime identity")
	}
}

func TestOpenGrepResolverPreservesReleaseFetchOptions(t *testing.T) {
	f := newScannerScriptFixture(t)
	output, err := f.run(t, "resolve-opengrep.sh", []string{"--artifact-dir-only", "--archive", "local release.tar.gz", "--offline"},
		"OPENGREP_CACHE_DIR=shared cache", "LYCAON_OPENGREP_CANDIDATE=/experimental")
	if err != nil {
		t.Fatalf("resolve local release: %v\n%s", err, output)
	}
	want := "go|candidate=|run|./cmd/opengrep-artifact|-mode|fetch|-cache-root|" + filepath.Join(f.root, "shared cache") +
		"|-archive|" + filepath.Join(f.root, "local release.tar.gz") + "|-offline\n"
	if calls := f.calls(t); calls != want || strings.TrimSpace(output) != f.artifact {
		t.Fatalf("fetch options changed: calls=%q want=%q output=%q", calls, want, output)
	}
}

func TestOpenGrepReleaseSelectionUsesExactDescriptor(t *testing.T) {
	for _, descriptor := range []string{"release descriptor.json", "https://github.com/paintedwolf-ai/paintedwolf-opengrep/releases/download/1.0.0/release.json"} {
		t.Run(descriptor, func(t *testing.T) {
			f := newScannerScriptFixture(t)
			output, err := f.run(t, "select-opengrep-release.sh", []string{"--tag", "v1.30.0+paintedwolf.33", "--expected-commit", strings.Repeat("a", 40), descriptor},
				"OPENGREP_CACHE_DIR=shared cache", "LYCAON_OPENGREP_CANDIDATE=/experimental")
			if err != nil {
				t.Fatalf("select release: %v\n%s", err, output)
			}
			resolvedDescriptor := descriptor
			if !strings.HasPrefix(descriptor, "https://") {
				resolvedDescriptor = filepath.Join(f.root, descriptor)
			}
			want := "go|candidate=|run|./cmd/opengrep-artifact|-mode|select|-release-manifest|" + resolvedDescriptor +
				"|-tag|v1.30.0+paintedwolf.33|-expected-commit|" + strings.Repeat("a", 40) +
				"|-manifest-output|" + filepath.Join(f.root, "lycaon/config/runtime/scanners/bundled-manifest.yaml") +
				"|-cache-root|" + filepath.Join(f.root, "shared cache") + "\n"
			if calls := f.calls(t); calls != want {
				t.Fatalf("selection changed descriptor or authority: calls=%q want=%q", calls, want)
			}
		})
	}
}

func TestOpenGrepStagingFetchesTheDeclaredTarget(t *testing.T) {
	f := newScannerScriptFixture(t)
	target := "x86_64-apple-darwin"
	output, err := f.run(t, "stage-bundled-opengrep.sh", []string{"relative stage", target})
	if err != nil {
		t.Fatalf("stage target: %v\n%s", err, output)
	}
	calls := f.calls(t)
	if !strings.Contains(calls, "|-mode|fetch|-cache-root|"+filepath.Join(f.paths.Bin, "opengrep-artifacts")+"|-target|"+target+"\n") || !strings.Contains(calls, "|-mode|stage|-artifact-directory|"+f.artifact+"|-root|"+filepath.Join(f.root, "relative stage")+"|-target|"+target+"\n") {
		t.Fatalf("declared platform was not used for fetch and stage:\n%s", calls)
	}
}

func TestOpenGrepResolverUsesOneTargetForFetchAndIdentity(t *testing.T) {
	f := newScannerScriptFixture(t)
	target := "aarch64-unknown-linux-gnu"
	output, err := f.run(t, "resolve-opengrep.sh", []string{"--identity-only", "--target", target})
	if err != nil {
		t.Fatalf("resolve target identity: %v\n%s", err, output)
	}
	calls := f.calls(t)
	if strings.Count(calls, "|-target|"+target+"\n") != 2 {
		t.Fatalf("fetch and identity target differed:\n%s", calls)
	}
}
