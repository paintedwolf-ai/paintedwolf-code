//go:build linux && nixworkflow

package confine_test

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

type nixWorkflowFixture struct {
	bin, bash, project, profile, store string
	env                                []string
	boundary                           confine.Confinement
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
  args = [ "-c" "printf fixture > \"$out\"" ];
  PATH = "${bash}/bin:${coreutils}/bin";
  shellHook = ''
    export PW_NIX_SELECTED=fixture
    printf invoked > %q
  '';
}
`
	source := fmt.Sprintf(expression, filepath.Dir(filepath.Dir(bash)), filepath.Dir(filepath.Dir(coreutils)), system, filepath.Join(project, "hook-invoked"))
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
	return nixWorkflowFixture{bin: bin, bash: bash, project: project, profile: profile, store: store, env: env, boundary: box}
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
		t.Fatalf("Nix workflow failed: result=%+v error=%v", result, err)
	}
	return bytes.TrimSpace(result.Output)
}
