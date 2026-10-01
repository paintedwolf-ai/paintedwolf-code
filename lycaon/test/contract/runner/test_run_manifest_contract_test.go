package contract

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type goRunManifest struct {
	Version         int                `json:"version"`
	Name            string             `json:"name"`
	SourceCommit    string             `json:"source_commit"`
	SourceRoot      string             `json:"source_root"`
	RunnerArgv      []string           `json:"runner_argv"`
	EffectiveGoArgv []string           `json:"effective_go_argv"`
	Environment     map[string]*string `json:"environment"`
	FailedPackages  []string           `json:"failed_packages"`
}

func nulFile(t *testing.T, path string, values ...string) {
	t.Helper()
	f, err := os.Create(path)
	contractcheck.FailErr(t, "create NUL file", err)
	for _, value := range values {
		_, err = f.Write(append([]byte(value), 0))
		contractcheck.FailErr(t, "write NUL file", err)
	}
	contractcheck.FailErr(t, "close NUL file", f.Close())
}

func TestGoTestRunManifestPreservesReplayInputs(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	helper := filepath.Join(root, "scripts", "test-run-manifest.py")
	tmp := t.TempDir()
	runnerArgv := filepath.Join(tmp, "runner.argv")
	goArgv := filepath.Join(tmp, "go.argv")
	manifestPath := filepath.Join(tmp, "manifest.json")
	failedPath := filepath.Join(tmp, "failed.txt")
	nulFile(t, runnerArgv, "--full", "--tags", "integration", "--label", "", "--", "./test/security/...", "-run", "TestJourney")
	nulFile(t, goArgv, "-json", "-timeout", "80m", "./test/security/...", "-run", "TestJourney")

	cmd := exec.Command("python3", helper,
		"create", "--output", manifestPath,
		"--id", "run-1", "--name", "test:security",
		"--source-commit", strings.Repeat("a", 40),
		"--source-ref", "refs/painted-wolf/test-failures/run-1",
		"--source-root", "/recorded/source",
		"--runner-argv", runnerArgv, "--go-argv", goArgv,
		"--env", "GO_TEST_P=2", "--env", "PW_TEST_TIMEOUT_SCALE=4",
		"--env", "LYCAON_OPENGREP_CANDIDATE=/candidate with spaces",
		"--env", "LYCAON_SCANNER_SUITE_REQUIRED=1", "--env", "PATH=/recorded/source/.bin:/scanner/bin",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create manifest: %v\n%s", err, output)
	}
	contractcheck.FailErr(t, "write failed packages", os.WriteFile(failedPath, []byte("example/b\nexample/a\nexample/a\n"), 0o600))
	cmd = exec.Command("python3", helper, "finalize", "--manifest", manifestPath, "--failed-packages", failedPath)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("finalize manifest: %v\n%s", err, output)
	}

	raw, err := os.ReadFile(manifestPath)
	contractcheck.FailErr(t, "read manifest", err)
	var manifest goRunManifest
	contractcheck.FailErr(t, "decode manifest", json.Unmarshal(raw, &manifest))
	if manifest.Version != 1 || manifest.Name != "test:security" || len(manifest.SourceCommit) != 40 || manifest.SourceRoot != "/recorded/source" {
		t.Fatalf("manifest identity = %+v", manifest)
	}
	wantArgv := []string{"--full", "--tags", "integration", "--label", "", "--", "./test/security/...", "-run", "TestJourney"}
	if strings.Join(manifest.RunnerArgv, "\x00") != strings.Join(wantArgv, "\x00") {
		t.Fatalf("runner argv = %#v, want %#v", manifest.RunnerArgv, wantArgv)
	}
	if got := manifest.Environment["GO_TEST_P"]; got == nil || *got != "2" {
		t.Fatalf("GO_TEST_P = %v", got)
	}
	if got := manifest.Environment["LYCAON_SCANNER_SUITE_REQUIRED"]; got == nil || *got != "1" {
		t.Fatalf("LYCAON_SCANNER_SUITE_REQUIRED = %v", got)
	}
	if got := manifest.Environment["PATH"]; got == nil || *got != "/recorded/source/.bin:/scanner/bin" {
		t.Fatalf("PATH = %v", got)
	}
	if got := strings.Join(manifest.FailedPackages, ","); got != "example/a,example/b" {
		t.Fatalf("failed packages = %q", got)
	}

	cmd = exec.Command("python3", helper, "go-argv", "--manifest", manifestPath)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("read effective Go argv: %v\n%s", err, output)
	}
	wantGoArgv := []string{"-json", "-timeout", "80m", "./test/security/...", "-run", "TestJourney"}
	if got := strings.TrimSuffix(string(output), "\x00"); got != strings.Join(wantGoArgv, "\x00") {
		t.Fatalf("effective Go argv = %q, want %#v", got, wantGoArgv)
	}

	runnerPath := filepath.Join(tmp, "replay-runner")
	replayRoot := filepath.Join(tmp, "current-source")
	replayArgvPath := filepath.Join(tmp, "replay.argv")
	replayEnvPath := filepath.Join(tmp, "replay.env")
	runner := `#!/bin/bash
printf '%s\0' "$@" > "$REPLAY_ARGV_CAPTURE"
printf '%s\n%s\n%s\n%s\n%s\n' "$GO_TEST_P" "$PW_TEST_TIMEOUT_SCALE" "$PATH" "$PW_TEST_REPLAY_MANIFEST" "$LYCAON_OPENGREP_CANDIDATE" > "$REPLAY_ENV_CAPTURE"
`
	contractcheck.FailErr(t, "write replay runner", os.WriteFile(runnerPath, []byte(runner), 0o700))
	contractcheck.FailErr(t, "create replay root", os.Mkdir(replayRoot, 0o700))
	cmd = exec.Command("python3", helper, "replay", "--manifest", manifestPath, "--runner", runnerPath, "--current-source-root", ".")
	cmd.Dir = replayRoot
	cmd.Env = append(os.Environ(),
		"REPLAY_ARGV_CAPTURE="+replayArgvPath,
		"REPLAY_ENV_CAPTURE="+replayEnvPath,
		"LYCAON_OPENGREP_CANDIDATE=/wrong inherited candidate",
	)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("replay manifest: %v\n%s", err, output)
	}
	replayedArgv, err := os.ReadFile(replayArgvPath)
	contractcheck.FailErr(t, "read replay argv", err)
	if got := strings.TrimSuffix(string(replayedArgv), "\x00"); got != strings.Join(wantArgv, "\x00") {
		t.Fatalf("replayed argv = %q, want %#v", got, wantArgv)
	}
	replayedEnv, err := os.ReadFile(replayEnvPath)
	contractcheck.FailErr(t, "read replay environment", err)
	physicalReplayRoot, err := filepath.EvalSymlinks(replayRoot)
	contractcheck.FailErr(t, "resolve replay root", err)
	wantEnv := strings.Join([]string{"2", "4", physicalReplayRoot + "/.bin:/scanner/bin", manifestPath, "/candidate with spaces", ""}, "\n")
	if got := string(replayedEnv); got != wantEnv {
		t.Fatalf("replayed environment = %q, want %q", got, wantEnv)
	}
}

func TestSourceSnapshotRunsFromCapturedCommit(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	script := filepath.Join(root, "scripts", "test-source-snapshot.sh")
	cmd := exec.Command("bash", script, "run", "--", "bash", "-c",
		`test -n "$PW_SOURCE_SNAPSHOT_COMMIT" && test -f Taskfile.yml && ! git symbolic-ref -q HEAD && `+
			`{ test ! -d "$PW_SOURCE_ROOT_ORIGINAL/lycaon-den/src-tauri/binaries" || `+
			`test -d lycaon-den/src-tauri/binaries; } && `+
			`{ test ! -f "$PW_SOURCE_ROOT_ORIGINAL/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md" || `+
			`test -f lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md; } && `+
			`{ test ! -x "$PW_SOURCE_ROOT_ORIGINAL/lycaon-den/src-tauri/engine-root/gitengine/bin/git" || `+
			`test -x lycaon-den/src-tauri/engine-root/gitengine/bin/git; } && printf '%s' "$PW_SOURCE_SNAPSHOT_COMMIT"`)
	cmd.Dir = root
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "PW_SOURCE_SNAPSHOT_COMMIT=") ||
			strings.HasPrefix(item, "PW_SOURCE_ROOT_ORIGINAL=") ||
			strings.HasPrefix(item, "PW_TEST_ARTIFACT_ROOT=") {
			continue
		}
		cmd.Env = append(cmd.Env, item)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run source snapshot: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); len(got) != 40 {
		t.Fatalf("snapshot commit = %q", got)
	}
}

func TestSourceSnapshotRejectsLiveCheckoutMarker(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	headCmd := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	head, err := headCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("read checkout head: %v\n%s", err, head)
	}
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "test-source-snapshot.sh"), "holding")
	cmd.Dir = root
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "PW_SOURCE_SNAPSHOT_COMMIT=") ||
			strings.HasPrefix(item, "PW_SOURCE_ROOT_ORIGINAL=") {
			continue
		}
		cmd.Env = append(cmd.Env, item)
	}
	cmd.Env = append(cmd.Env,
		"PW_SOURCE_ROOT_ORIGINAL="+root,
		"PW_SOURCE_SNAPSHOT_COMMIT="+strings.TrimSpace(string(head)),
	)
	if err := cmd.Run(); err == nil {
		t.Fatal("live checkout accepted a source snapshot marker")
	}
}

func TestDigestRunsFromCapturedSource(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	want := os.Getenv("PW_SOURCE_SNAPSHOT_COMMIT")
	if want == "" {
		t.Fatal("PW_SOURCE_SNAPSHOT_COMMIT is empty")
	}
	cmd := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("read test source commit: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != want {
		t.Fatalf("test source commit = %q, want %q", got, want)
	}
}
