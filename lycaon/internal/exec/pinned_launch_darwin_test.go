//go:build darwin

package exec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
)

type pinnedLaunchFixture struct {
	directory string
	allowed   map[string]bool
}

func (p pinnedLaunchFixture) OpenExecutable(ctx context.Context, name string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(p.directory, name)
	}
	if !p.allowed[filepath.Clean(name)] {
		return nil, os.ErrPermission
	}
	return os.Open(name)
}
func (p pinnedLaunchFixture) OpenCwd(ctx context.Context) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.Open(p.directory)
}

func TestPinnedLaunchPreservesOrdinaryScriptArgumentsAndDirectory(t *testing.T) {
	for _, host := range []bool{false, true} {
		t.Run(map[bool]string{false: "confined", true: "explicit host execution"}[host], func(t *testing.T) {
			directory := t.TempDir()
			entry := filepath.Join(directory, "task")
			script := []byte("#!/bin/sh -e\nprintf '%s\\n' \"$0\" \"$1\"\nprintf 'payload' > result\n")
			if err := os.WriteFile(entry, script, 0700); err != nil {
				t.Fatal(err)
			}
			paths := pinnedLaunchFixture{directory, map[string]bool{entry: true, "/bin/sh": true}}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			confinement := &confine.Confinement{Roots: []string{directory}, Network: confine.NetworkDeny, HostExecution: host}
			plan := AgentLaunch(LaunchAgentCommand, "synthetic pinned script fixture", confinement).WithPathAuthority(paths)
			cmd, cleanup, err := PrepareCommand(ctx, "./task", []string{"literal argument"}, ExecOpts{Launch: plan, Dir: directory, Env: []string{"PATH=/usr/bin:/bin"}})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := RunInOwnGroup(cmd); err != nil {
				t.Fatalf("pinned launch: %v; diagnostic %s", err, stderr.String())
			}
			if stdout.String() != "./task\nliteral argument\n" {
				t.Fatalf("script argv changed: %q", stdout.String())
			}
			data, err := os.ReadFile(filepath.Join(directory, "result"))
			if err != nil || string(data) != "payload" {
				t.Fatalf("script cwd/effect mismatch: %q %v", data, err)
			}
		})
	}
}

func TestPinnedLaunchRefusesChangedEntryAndRevokedAdmissionBeforePayload(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "replaced entry", true: "revoked admission"}[revoke], func(t *testing.T) {
			directory := t.TempDir()
			entry := filepath.Join(directory, "task")
			original := []byte("#!/bin/sh\nprintf payload > result\n")
			if err := os.WriteFile(entry, original, 0700); err != nil {
				t.Fatal(err)
			}
			paths := pinnedLaunchFixture{directory, map[string]bool{entry: true, "/bin/sh": true}}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			plan := AgentLaunch(LaunchAgentCommand, "synthetic launch revalidation", &confine.Confinement{HostExecution: true}).WithPathAuthority(paths)
			cmd, cleanup, err := PrepareCommand(ctx, "./task", nil, ExecOpts{Launch: plan, Dir: directory, Env: []string{"PATH=/usr/bin:/bin"}})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if revoke {
				delete(paths.allowed, entry)
			} else {
				if err := os.Rename(entry, entry+".held"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf replacement > result\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			err = RunInOwnGroup(cmd)
			if revoke && !errors.Is(err, os.ErrPermission) {
				t.Fatalf("current authority refusal lost: %v", err)
			}
			if !revoke && !errors.Is(err, confine.ErrLaunchIdentityChanged) {
				t.Fatalf("changed entry admitted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(directory, "result")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("payload ran before refusal: %v", err)
			}
		})
	}
}

func TestPinnedLaunchDoesNotGrantAnUnadmittedHardlinkOperand(t *testing.T) {
	directory := t.TempDir()
	entry := filepath.Join(directory, "task")
	alias := filepath.Join(directory, "unadmitted")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf payload\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(entry, alias); err != nil {
		t.Fatal(err)
	}
	paths := pinnedLaunchFixture{directory, map[string]bool{entry: true, "/bin/sh": true}}
	plan := AgentLaunch(LaunchAgentCommand, "synthetic alias admission", &confine.Confinement{HostExecution: true}).WithPathAuthority(paths)
	cmd, cleanup, err := PrepareCommand(t.Context(), alias, nil, ExecOpts{Launch: plan})
	if cleanup != nil {
		cleanup()
	}
	if cmd != nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("held image granted another operand: %v", err)
	}
}

func TestPinnedHostExecutionPreservesSetuidProcessInspection(t *testing.T) {
	paths := pinnedLaunchFixture{t.TempDir(), map[string]bool{"/bin/ps": true}}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	plan := AgentLaunch(LaunchAgentCommand, "synthetic privileged inspection", &confine.Confinement{HostExecution: true}).WithPathAuthority(paths)
	pid := strconv.Itoa(os.Getpid())
	cmd, cleanup, err := PrepareCommand(ctx, "/bin/ps", []string{"-p", pid, "-o", "pid="}, ExecOpts{Launch: plan, Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var output, diagnostic bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &diagnostic
	if err := RunInOwnGroup(cmd); err != nil {
		t.Fatalf("privileged launch: %v; diagnostic %s", err, diagnostic.String())
	}
	if strings.TrimSpace(output.String()) != pid {
		t.Fatalf("process inspection output %q", output.String())
	}
}
