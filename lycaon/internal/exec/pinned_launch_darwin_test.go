//go:build darwin && cgo

package exec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	if !p.allowed[name] {
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
			pins, err := confine.CaptureProcessPins(t.Context(), entry, paths, confine.LaunchVectorLimits{ArgumentBytes: 128 << 10, ArgvBytes: 1 << 20, EnvironmentBytes: 1 << 20, ExpandedFields: 16384})
			if err != nil {
				t.Fatal(err)
			}
			defer pins.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			confinement := &confine.Confinement{Roots: []string{directory}, Network: confine.NetworkDeny, HostExecution: host}
			plan := AgentLaunch(LaunchAgentCommand, "synthetic pinned script fixture", confinement).WithPinnedIdentity(pins)
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

func TestRequiredPinsRefuseMissingCaptureBeforeLaunch(t *testing.T) {
	plan := AgentLaunch(LaunchAgentCommand, "synthetic missing capture", &confine.Confinement{HostExecution: true}).WithPinnedIdentity(nil)
	cmd, cleanup, err := PrepareCommand(t.Context(), "/bin/echo", nil, ExecOpts{Launch: plan})
	if cleanup != nil {
		cleanup()
	}
	if cmd != nil || err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("missing required capture admitted")
	}
}
