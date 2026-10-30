//go:build linux

package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

type linuxPinnedPayload struct {
	Args               []string
	Cwd, AuxExecutable string
	ProtocolFDs        []int
}

func TestPreparedLinuxBinaryPreservesNativePathArgvCwdAndExecuteOnlyMode(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	// Execute-only images are intentionally non-dumpable. Read AT_EXECFN from
	// the process's initial auxiliary vector rather than reopening /proc/self.
	self := filepath.Join(t.TempDir(), "native-payload")
	compile := osexec.CommandContext(t.Context(), "cc", "testdata/pinned_payload.c", "-o", self)
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("build native payload fixture: %v: %s", err, output)
	}
	image, err := os.ReadFile(self)
	testutil.FailErr(t, "read source fixture", err)
	root := t.TempDir()
	testutil.FailErr(t, "create relative binary directory", os.Mkdir(filepath.Join(root, "bin"), 0700))
	for _, dir := range []string{root, filepath.Join(root, "bin")} {
		testutil.FailErr(t, "write execute-only native fixture", os.WriteFile(filepath.Join(dir, "synthetic-argv-zero"), image, 0100))
	}
	for _, fixture := range []struct{ label, name, path, execfn string }{
		{"absolute PATH", "synthetic-argv-zero", root + ":/usr/bin:/bin", filepath.Join(root, "synthetic-argv-zero")},
		{"dot slash", "./synthetic-argv-zero", "/usr/bin:/bin", "./synthetic-argv-zero"},
		{"subpath", "bin/synthetic-argv-zero", "/usr/bin:/bin", "bin/synthetic-argv-zero"},
		{"relative PATH", "synthetic-argv-zero", "bin:/usr/bin:/bin", "bin/synthetic-argv-zero"},
		{"empty PATH", "synthetic-argv-zero", ":/usr/bin:/bin", "synthetic-argv-zero"},
	} {
		for _, confined := range []bool{false, true} {
			name := fixture.label
			if confined {
				name += "/confined"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				args := []string{"-test.run=^TestPinnedLinuxPayload$", "--", "literal space", "--launch-ready-fd", "user data"}
				plan := HostLaunch("synthetic native binary fixture")
				if confined {
					plan = AgentLaunch(LaunchAgentCommand, "synthetic native binary fixture", &confine.Confinement{Roots: []string{root}, Network: confine.NetworkDeny}).WithPathAuthority(confine.NativeLaunchPaths(root))
				}
				cmd, cleanup, err := PrepareCommand(ctx, fixture.name, args, ExecOpts{Launch: plan, Dir: root, Env: []string{"PATH=" + fixture.path}})
				testutil.FailErr(t, "prepare native filename fixture", err)
				defer cleanup()
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if err := RunInOwnGroup(ctx, cmd); err != nil {
					t.Fatalf("native launch: %v; diagnostic %s; stdout %s", err, stderr.String(), stdout.String())
				}
				var payload linuxPinnedPayload
				testutil.FailErr(t, "decode actual native facts", json.Unmarshal(stdout.Bytes(), &payload))
				if !slices.Equal(payload.Args, append([]string{fixture.name}, args...)) || payload.Cwd != root || len(payload.ProtocolFDs) != 0 {
					t.Fatalf("native facts changed: %+v", payload)
				}
				if payload.AuxExecutable != fixture.execfn {
					t.Fatalf("native AT_EXECFN spelling changed: %q", payload.AuxExecutable)
				}
			})
		}
	}
}

func TestPreparedLinuxScriptPreservesOrdinaryPathAndNativeArgv(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "fixture-script")
	testutil.FailErr(t, "write script", os.WriteFile(entry, []byte("#!/bin/sh -e\nprintf '%s\\n' \"$0\" \"$1\"\nprintf exact > effect\n"), 0700))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	plan := AgentLaunch(LaunchAgentCommand, "synthetic native script fixture", &confine.Confinement{HostExecution: true}).WithPathAuthority(confine.NativeLaunchPaths(root))
	cmd, cleanup, err := PrepareCommand(ctx, "./fixture-script", []string{"literal space"}, ExecOpts{Launch: plan, Dir: root, Env: []string{"PATH=/usr/bin:/bin"}})
	testutil.FailErr(t, "prepare ordinary script", err)
	defer cleanup()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	testutil.FailErr(t, "launch through native pathname", RunInOwnGroup(t.Context(), cmd))
	if stdout.String() != "./fixture-script\nliteral space\n" {
		t.Fatalf("script argv changed: %q", stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "effect"))
	testutil.FailErr(t, "read actual script effect", err)
	if string(data) != "exact" {
		t.Fatal("script actual cwd/effect changed")
	}
}
