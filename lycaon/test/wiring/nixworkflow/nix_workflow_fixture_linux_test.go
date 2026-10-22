//go:build linux && nixworkflow

package nixworkflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

type nixWorkflowFixture struct {
	bin, bash, project, flake, profile, store string
	env                                       []string
	boundary                                  confine.Confinement
}

func nixStoreFixtureInput(t *testing.T, key, suffix string) string {
	t.Helper()
	value := os.Getenv(key)
	if !filepath.IsAbs(value) || !strings.HasPrefix(value, "/nix/store/") || !strings.HasSuffix(value, suffix) {
		t.Fatalf("%s must identify the prepared locked Nix store input ending %s", key, suffix)
	}
	info, err := os.Stat(value)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("required prepared Nix fixture input %s: %v", key, err)
	}
	return value
}

func prepareNixWorkflows(t *testing.T) nixWorkflowFixture {
	t.Helper()
	bin, env := nixFixture(t)
	bash := nixStoreFixtureInput(t, "PW_NIX_FIXTURE_BASH", "/bin/bash")
	coreutils := nixStoreFixtureInput(t, "PW_NIX_FIXTURE_COREUTILS", "/bin/mkdir")
	stdenv := os.Getenv("PW_NIX_FIXTURE_STDENV")
	info, err := os.Stat(filepath.Join(stdenv, "setup"))
	if !strings.HasPrefix(stdenv, "/nix/store/") || err != nil || !info.Mode().IsRegular() {
		t.Fatalf("PW_NIX_FIXTURE_STDENV must identify the prepared locked standard environment: %v", err)
	}
	project := t.TempDir()
	system := map[string]string{"amd64": "x86_64-linux", "arm64": "aarch64-linux"}[runtime.GOARCH]
	if system == "" {
		t.Fatal("Nix workflow qualification requires a supported native architecture")
	}
	const expression = `{}:
let bash = builtins.storePath %q; coreutils = builtins.storePath %q;
in builtins.derivation {
  name = "paintedwolf-offline-workflow-fixture";
  system = %q;
  builder = "${bash}/bin/bash";
  outputs = [ "out" ];
  stdenv = builtins.storePath %q;
  args = [ "-c" "printf fixture > \"$out\"" ];
  PATH = "${bash}/bin:${coreutils}/bin";
  shellHook = ''
    export PW_NIX_SELECTED=fixture
    printf invoked > %q
  '';
}
`
	source := fmt.Sprintf(expression, filepath.Dir(filepath.Dir(bash)), filepath.Dir(filepath.Dir(coreutils)), system, stdenv, filepath.Join(project, "hook-invoked"))
	testutil.FailErr(t, "write closed non-flake fixture", os.WriteFile(filepath.Join(project, "shell.nix"), []byte(source), 0o600))
	flake := fmt.Sprintf(`{ outputs = { self }: { devShells.%s.default = import ./shell.nix {}; apps.%s.default = { type = "app"; program = %q; }; }; }`, system, system, bash)
	testutil.FailErr(t, "write local input-free flake", os.WriteFile(filepath.Join(project, "flake.nix"), []byte(flake), 0o600))
	testutil.FailErr(t, "write fixed empty input lock", os.WriteFile(filepath.Join(project, "flake.lock"), []byte(`{"nodes":{"root":{}},"root":"root","version":7}`), 0o600))
	profile := filepath.Join(project, "cached-environment")
	for index, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			env[index] = "PATH=" + filepath.Dir(bash) + ":" + filepath.Dir(coreutils) + ":" + strings.TrimPrefix(entry, "PATH=")
		}
	}
	env = append(env, "NIX_BUILD_SHELL="+bash, "PW_NIX_INHERITED=person-value")
	// Only this declared fixture is prepared outside the application boundary. No downloads or remote builders.
	nativeNix(t, bin, env, "--offline", "--option", "max-jobs", "1", "--option", "cores", "1", "print-dev-env", "--impure", "--no-write-lock-file", "--profile", profile, project)
	var metadata struct{ Path string }
	testutil.FailErr(t, "parse prepared flake source", json.Unmarshal(nativeNix(t, bin, env, "--offline", "flake", "metadata", "--json", "--no-write-lock-file", project), &metadata))
	if !strings.HasPrefix(metadata.Path, "/nix/store/") {
		t.Fatalf("flake preparation did not produce an immutable source: %s", metadata.Path)
	}
	if _, err := os.Stat(filepath.Join(project, "hook-invoked")); !os.IsNotExist(err) {
		t.Fatal("environment preparation executed the project shell hook")
	}
	state := immutableNixState(t)
	store := "local?" + (url.Values{"state": {state}, "read-only": {"true"}}).Encode()
	box := denyConfinement(project)
	for _, entry := range env {
		if home, ok := strings.CutPrefix(entry, "HOME="); ok {
			box.Roots = append(box.Roots, home)
		}
	}
	box.ReadRoots = []string{state, "/nix/store"}
	return nixWorkflowFixture{bin: bin, bash: bash, project: project, flake: "path:" + metadata.Path, profile: profile, store: store, env: env, boundary: box}
}

func (fixture nixWorkflowFixture) run(t *testing.T, ctx context.Context, kind execpkg.LaunchKind, bin string, arguments ...string) (*execpkg.PipelineResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return execpkg.RunPipeline(ctx, []execpkg.Stage{{Name: bin, Args: arguments}}, execpkg.ExecOpts{
		Launch: execpkg.AgentLaunch(kind, "nix_workflow_fixture", &fixture.boundary), Dir: fixture.project,
		Env: fixture.env, SeparateStderr: true, MaxOutputBytes: 64 << 10,
	})
}

func nixResult(t *testing.T, result *execpkg.PipelineResult, err error) []byte {
	t.Helper()
	if err != nil || result == nil || result.ExitCode != 0 || result.TimedOut {
		if result != nil {
			t.Logf("Nix stdout=%s\nstderr=%s", result.Output, result.Stderr)
		}
		t.Fatalf("Nix workflow failed: result=%+v error=%v", result, err)
	}
	return bytes.TrimSpace(result.Output)
}

// The integration fixture owns its action feed and helper lifetime through public APIs.
func TestMain(m *testing.M) {
	confine.SetKeyMaterialPathsSource(func() []string {
		return []string{"~/.ssh/", "~/.gnupg/", "~/.age/", "~/.config/sops/age/", "~/.docker/trust/", "~/Library/Keychains/", "~/.password-store/", "~/.local/share/keyrings/"}
	})
	code := m.Run()
	confine.StopRefusalWatch()
	if helperBuild.directory != "" {
		if err := os.RemoveAll(helperBuild.directory); err != nil {
			fmt.Fprintln(os.Stderr, "remove Nix workflow helper directory:", err)
			code = 1
		}
	}
	os.Exit(code)
}

var helperBuild struct {
	once      sync.Once
	directory string
	path      string
	err       error
}

func requireLinuxSandbox(t *testing.T) string {
	t.Helper()
	caps, _ := confine.LinuxCapabilities()
	if !caps.Available() {
		report, _ := json.MarshalIndent(caps, "", "  ")
		t.Fatalf("linux confinement unavailable on this host:\n%s", report)
	}
	self, err := os.Executable()
	testutil.FailErr(t, "resolve Nix workflow process helper", err)
	if dir := os.Getenv("PW_GO_CHILD_COVERDIR"); dir != "" {
		helperBuild.once.Do(func() {
			helperBuild.err = os.Setenv("GOCOVERDIR", dir) //nolint:usetesting // All fixture children share the coverage helper build.
			if helperBuild.err != nil {
				return
			}
			helperBuild.directory, helperBuild.err = os.MkdirTemp("", "pw-nix-helper-") //nolint:usetesting // TestMain owns the helper across individual tests.
			if helperBuild.err != nil {
				return
			}
			helperBuild.path = filepath.Join(helperBuild.directory, "confine-helper")
			build := exec.Command("go", "build", "-cover", "-tags", "confinehelpermain", "-coverpkg=github.com/lycaon/lycaon/internal/confine/...", "-o", helperBuild.path,
				filepath.Join(testutil.CheckoutRoot(t), "lycaon/internal/confine/testdata/helpermain"))
			if out, err := build.CombinedOutput(); err != nil && helperBuild.err == nil {
				helperBuild.err = fmt.Errorf("build coverage helper: %w: %s", err, out)
			}
		})
		testutil.FailErr(t, "build Nix workflow coverage helper", helperBuild.err)
		return helperBuild.path
	}
	return self
}

func denyConfinement(roots ...string) confine.Confinement {
	if dir := os.Getenv("PW_GO_CHILD_COVERDIR"); dir != "" {
		roots = append(roots, dir)
	}
	return confine.Confinement{Roots: roots, Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride}
}

func linuxConfinedRun(t *testing.T, self string, c confine.Confinement, env []string, name string, args ...string) (syscall.WaitStatus, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd, cleanup, err := confine.Command(ctx, self, name, args, c)
	testutil.FailErr(t, "launch Nix workflow action", err)
	defer cleanup()
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	_ = cmd.Run()
	if cmd.ProcessState == nil {
		t.Fatalf("%s did not start: %s", name, out.String())
	}
	return cmd.ProcessState.Sys().(syscall.WaitStatus), out.String()
}

func boundConfinement(t *testing.T, c confine.Confinement) (*confine.Confinement, *confine.ActionLease) {
	t.Helper()
	confine.StartRefusalWatch(context.Background(), t.TempDir())
	t.Cleanup(confine.StopRefusalWatch)
	lease, err := confine.BindAction(&c, confine.EgressCommand{ToolCallID: "call-" + t.Name(), ToolName: "command", CommandLine: "probe"})
	testutil.FailErr(t, "bind Nix workflow action", err)
	t.Cleanup(func() { lease.Close(context.Background()) })
	if w := lease.Refusals().Witness; w != confine.WitnessIncomplete {
		t.Fatalf("Nix workflow action has refusal witness %q", w)
	}
	return &c, lease
}

func refusalTargets(r confine.SandboxRefusals) map[string]confine.RefusalRecovery {
	out := map[string]confine.RefusalRecovery{}
	for _, x := range r.Refusals {
		out[x.Operation+" "+x.Target] = x.Recovery
	}
	return out
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pw-nix-") //nolint:usetesting // Keep actual Unix socket fixture names within the kernel path limit.
	testutil.FailErr(t, "create short Nix workflow socket fixture", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
