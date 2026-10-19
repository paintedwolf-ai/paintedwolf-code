//go:build linux && nixworkflow

package confine_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxNixTerminalAndBackgroundToolchains(t *testing.T) {
	fixture := prepareNixWorkflows(t)
	args := []string{"--log-format", "raw", "--offline", "--store", fixture.store, "shell", filepath.Dir(filepath.Dir(fixture.bash)), "--command", fixture.bash, "-c", `printf '%s\n' "$BASH"`}
	launch := execpkg.AgentLaunch(execpkg.LaunchAgentCommand, "nix_terminal_fixture", &fixture.boundary)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	terminal, err := execpkg.StartPTY(ctx, fixture.bin, args, execpkg.PTYOpts{Launch: launch, Dir: fixture.project, Env: fixture.env})
	testutil.FailErr(t, "start actual selected Nix terminal", err)
	t.Cleanup(func() { _ = terminal.Close() })
	output, readErr := io.ReadAll(terminal)
	if readErr != nil && !errors.Is(readErr, syscall.EIO) {
		t.Fatalf("read selected Nix terminal: %v", readErr)
	}
	testutil.FailErr(t, "wait actual selected Nix terminal", terminal.Wait())
	if strings.TrimSpace(string(output)) != fixture.bash {
		t.Fatalf("terminal selected shell=%q; want %s", output, fixture.bash)
	}
	var stdout, stderr bytes.Buffer
	job, err := execpkg.StartPipelineAsync(ctx, []execpkg.Stage{{Name: fixture.bin, Args: args}}, execpkg.ExecOpts{
		Launch: launch, Dir: fixture.project, Env: fixture.env, Timeout: 20 * time.Second,
	}, &stdout, &stderr)
	testutil.FailErr(t, "start actual selected Nix background job", err)
	result, err := job.Wait()
	nixResult(t, result, err)
	if strings.TrimSpace(stdout.String()) != fixture.bash {
		t.Fatalf("background selected shell=%q; stderr=%s", stdout.String(), stderr.String())
	}
}

func TestLinuxNixClientCancellationIsActionScoped(t *testing.T) {
	fixture := prepareNixWorkflows(t)
	coreutils := nixStoreFixtureInput(t, "PW_NIX_FIXTURE_COREUTILS", "/bin/mkdir")
	sleep := filepath.Join(filepath.Dir(coreutils), "sleep")
	for _, hostExecution := range []bool{false, true} {
		t.Run(fmt.Sprintf("host execution %t", hostExecution), func(t *testing.T) {
			box := fixture.boundary
			box.HostExecution = hostExecution
			boundary := confine.BoundaryOf(&box)
			if boundary.HostExecution != hostExecution || boundary.Applied == hostExecution {
				t.Fatalf("selected action misstated its boundary: %+v", boundary)
			}
			store := fixture.store
			if hostExecution {
				store = "daemon"
			}
			args := []string{"--offline", "--store", store, "shell", filepath.Dir(filepath.Dir(fixture.bash)), "--command", fixture.bash,
				"-c", `printf 'nix-ready\n'; exec "$1" 30`, "nix-lifetime", sleep}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader, writer := io.Pipe()
			defer func() { _ = reader.Close(); _ = writer.Close() }()
			var stderr bytes.Buffer
			job, err := execpkg.StartPipelineAsync(ctx, []execpkg.Stage{{Name: fixture.bin, Args: args}}, execpkg.ExecOpts{
				Launch: execpkg.AgentLaunch(execpkg.LaunchAgentCommand, "nix_cancellation_fixture", &box), Dir: fixture.project, Env: fixture.env, NoTimeout: true,
			}, writer, &stderr)
			testutil.FailErr(t, "start selected Nix lifetime action", err)
			defer func() { job.Kill(); _, _ = job.Wait() }()
			ready := make(chan error, 1)
			go func() {
				line, err := bufio.NewReader(reader).ReadString('\n')
				if err == nil && line != "nix-ready\n" {
					err = fmt.Errorf("unexpected fixture synchronization record %q", line)
				}
				ready <- err
			}()
			select {
			case err := <-ready:
				testutil.FailErr(t, "selected toolchain reached its lifetime barrier", err)
			case <-time.After(10 * time.Second):
				t.Fatal("selected Nix client did not reach its controlled lifetime barrier")
			}
			cancel()
			result, err := job.Wait()
			if result == nil || len(result.Stages) == 0 || result.ExitCode == 0 || (err == nil && !result.Stages[0].Failed) {
				t.Fatalf("canceled selected Nix client reported success: %+v, %v", result, err)
			}
		})
	}
}
