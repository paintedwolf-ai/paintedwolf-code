//go:build darwin || linux

package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPreparedLaunchFreezesFinalPathEnvironmentAndLiteralArguments(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, dir := range []string{first, second} {
		testutil.FailErr(t, "create PATH fixture", os.Mkdir(dir, 0700))
	}
	for _, pair := range [][2]string{{first, "selected"}, {second, "wrong"}} {
		testutil.FailErr(t, "create selected program", os.WriteFile(filepath.Join(pair[0], "portable-fixture"), []byte("#!/bin/sh\nprintf '%s\\n' "+pair[1]+" \"$1\" \"$VALUE\"\n"), 0700))
	}
	environment := []string{"PATH=" + first + ":/usr/bin:/bin", "VALUE=captured"}
	arguments := []string{"literal value"}
	cmd, cleanup, err := PrepareCommand(t.Context(), "portable-fixture", arguments, ExecOpts{Launch: HostLaunch("synthetic final PATH fixture"), Dir: root, Env: environment})
	testutil.FailErr(t, "prepare selected PATH result", err)
	defer cleanup()
	environment[0] = "PATH=" + second + ":/usr/bin:/bin"
	environment[1] = "VALUE=changed"
	arguments[0] = "changed"
	var output bytes.Buffer
	cmd.Stdout = &output
	testutil.FailErr(t, "run immutable selected invocation", RunPrepared(cmd))
	if output.String() != "selected\nliteral value\ncaptured\n" {
		t.Fatalf("selection or frozen vectors changed: %q", output.String())
	}
}

func TestPreparedLaunchRejectsChangedCapturedVectorsBeforeNativeStart(t *testing.T) {
	for _, field := range []string{"argv", "environment", "cwd", "helper", "descriptor"} {
		t.Run(field, func(t *testing.T) {
			cmd, cleanup, err := PrepareCommand(t.Context(), "/bin/echo", []string{"exact"}, ExecOpts{Launch: HostLaunch("synthetic immutable input fixture")})
			testutil.FailErr(t, "prepare immutable input", err)
			defer cleanup()
			switch field {
			case "helper":
				cmd.Path = "/bin/echo"
			case "descriptor":
				cmd.ExtraFiles[0] = nil
			case "argv":
				cmd.Args[len(cmd.Args)-1] = "changed"
			case "environment":
				cmd.Env = append(cmd.Env, "EXTRA=changed")
			case "cwd":
				cmd.Dir = t.TempDir()
			}
			if err := StartPrepared(cmd); !errors.Is(err, confine.ErrLaunchIdentityChanged) || cmd.Process != nil {
				t.Fatal("changed input reached native start")
			}
		})
	}
}

func TestPreparedLaunchRegistersBeforePayloadAndRechecksCurrentAuthority(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "fixture")
	effect := filepath.Join(root, "effect")
	testutil.FailErr(t, "write native effect fixture", os.WriteFile(entry, []byte("#!/bin/sh\nprintf exact > effect\n"), 0700))
	checks := 0
	plan := HostLaunch("synthetic current handoff fixture")
	plan.CurrentAuthority = func(context.Context) error { checks++; return nil }
	cmd, cleanup, err := PrepareCommand(t.Context(), entry, nil, ExecOpts{Launch: plan, Dir: root})
	testutil.FailErr(t, "prepare handoff fixture", err)
	defer cleanup()
	registered := false
	err = StartPreparedWith(cmd, cmd.Start, func(pid int) func() {
		if _, err := os.Stat(effect); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("payload ran before owner registration")
		}
		registered = true
		return TrackProcess(pid)
	})
	testutil.FailErr(t, "register and release actual child", err)
	testutil.FailErr(t, "settle actual child", cmd.Wait())
	data, err := os.ReadFile(effect)
	testutil.FailErr(t, "observe actual effect", err)
	if !registered || checks < 2 || string(data) != "exact" {
		t.Fatal("actual authority/ownership/effect handoff incomplete")
	}
}

func TestPreparedNativeCallbackFailureAndCancellationNeverReleasePayload(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "post-start transport failure", true: "cancelled callback"}[cancelled], func(t *testing.T) {
			root := t.TempDir()
			entry := filepath.Join(root, "fixture")
			testutil.FailErr(t, "write never-release fixture", os.WriteFile(entry, []byte("#!/bin/sh\nprintf forbidden > effect\n"), 0700))
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd, cleanup, err := PrepareCommand(ctx, entry, nil, ExecOpts{Launch: HostLaunch("synthetic retained native callback fixture"), Dir: root})
			testutil.FailErr(t, "prepare retained callback fixture", err)
			defer cleanup()
			fault := errors.New("synthetic native transport failure")
			released := 0
			err = StartPreparedWith(cmd, func() error {
				if err := cmd.Start(); err != nil {
					return err
				}
				if cancelled {
					cancel()
					return nil
				}
				return fault
			}, func(pid int) func() { untrack := TrackProcess(pid); return func() { released++; untrack() } })
			want := fault
			if cancelled {
				want = context.Canceled
			}
			if !errors.Is(err, want) || cmd.ProcessState == nil || released != 1 {
				t.Fatalf("actual failed child not settled once: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "effect")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed callback released user payload")
			}
			cleanup()
			if released != 1 {
				t.Fatal("failed child ownership released twice")
			}
		})
	}
}

func TestPreparedLaunchResolvesRelativeAndEmptyPathAtCapturedCwd(t *testing.T) {
	for _, pathValue := range []string{"bin:/usr/bin:/bin", ":/usr/bin:/bin"} {
		t.Run(pathValue, func(t *testing.T) {
			root := t.TempDir()
			dir := root
			if pathValue[0] != ':' {
				dir = filepath.Join(root, "bin")
				testutil.FailErr(t, "create relative PATH directory", os.Mkdir(dir, 0700))
			}
			testutil.FailErr(t, "write child PATH fixture", os.WriteFile(filepath.Join(dir, "portable-child-path-fixture"), []byte("#!/bin/sh\nprintf '%s' \"$0\""), 0700))
			cmd, cleanup, err := PrepareCommand(t.Context(), "portable-child-path-fixture", nil, ExecOpts{Launch: HostLaunch("synthetic child PATH fixture"), Dir: root, Env: []string{"PATH=" + pathValue}})
			testutil.FailErr(t, "prepare relative PATH", err)
			defer cleanup()
			var output bytes.Buffer
			cmd.Stdout = &output
			testutil.FailErr(t, "run child-selected PATH entry", RunPrepared(cmd))
			expected := "bin/portable-child-path-fixture"
			if pathValue[0] == ':' {
				expected = "portable-child-path-fixture"
			}
			if output.String() != expected {
				t.Fatalf("child PATH spelling changed: %q", output.String())
			}
		})
	}
}

func TestAgentPreparedLaunchRequiresCurrentExecutionAuthority(t *testing.T) {
	plan := AgentLaunch(LaunchAgentCommand, "synthetic missing authority", &confine.Confinement{HostExecution: true})
	cmd, cleanup, err := PrepareCommand(t.Context(), "/bin/echo", nil, ExecOpts{Launch: plan})
	if cleanup != nil {
		cleanup()
	}
	if err == nil || cmd != nil {
		t.Fatal("missing initiating execution authority admitted")
	}
}

func TestAgentPreparedLaunchCurrentActionRefusalNeverReleasesPayload(t *testing.T) {
	for _, change := range []string{"closed", "boundary", "actor", "root", "raw command", "raw boundary"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			entry := filepath.Join(root, "fixture")
			testutil.FailErr(t, "write action fixture", os.WriteFile(entry, []byte("#!/bin/sh\nprintf forbidden > effect\n"), 0700))
			boundary := &confine.Confinement{HostExecution: true, Roots: []string{root}}
			identity := confine.EgressCommand{SessionID: "synthetic-session", ToolCallID: "synthetic-action", ProjectDir: root}
			if change == "raw command" {
				identity.CommandLine = "\xff"
			}
			if change == "raw boundary" {
				boundary.ProjectID = "\xff"
			}
			lease, err := confine.BindAction(boundary, identity)
			testutil.FailErr(t, "admit actual action", err)
			defer lease.Close(context.Background())
			plan := AgentLaunch(LaunchAgentCommand, "synthetic actual execution authority", boundary)
			plan.CurrentAuthority = func(ctx context.Context) error { return lease.ValidateLaunch(ctx, boundary, identity) }
			cmd, cleanup, err := PrepareCommand(t.Context(), entry, nil, ExecOpts{Launch: plan, Dir: root})
			testutil.FailErr(t, "capture actual admitted action", err)
			defer cleanup()
			err = StartPreparedWith(cmd, cmd.Start, func(pid int) func() {
				switch change {
				case "raw command":
					identity.CommandLine = "\xfe"
				case "raw boundary":
					boundary.ProjectID = "\xfe"
				case "closed":
					lease.Close(context.Background())
				case "boundary":
					boundary.Roots[0] = filepath.Join(root, "changed")
				case "actor":
					identity.SessionID = "other-session"
				case "root":
					identity.ProjectDir = filepath.Join(root, "other")
				}
				return TrackProcess(pid)
			})
			if err == nil || cmd.ProcessState == nil {
				t.Fatal("changed actual action was not refused and reaped")
			}
			if _, err := os.Stat(filepath.Join(root, "effect")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("changed action released payload")
			}
		})
	}
}

func TestAgentPreparedLaunchPreservesExecuteOnlyNativeBinary(t *testing.T) {
	root := t.TempDir()
	self, err := os.Executable()
	testutil.FailErr(t, "locate native test image", err)
	data, err := os.ReadFile(self)
	testutil.FailErr(t, "read native fixture image", err)
	entry := filepath.Join(root, "execute-only")
	testutil.FailErr(t, "write execute-only native fixture", os.WriteFile(entry, data, 0100))
	boundary := &confine.Confinement{HostExecution: true}
	identity := confine.EgressCommand{SessionID: "synthetic-session", ToolCallID: "execute-only", ProjectDir: root}
	lease, err := confine.BindAction(boundary, identity)
	testutil.FailErr(t, "admit execute-only action", err)
	defer lease.Close(context.Background())
	plan := AgentLaunch(LaunchAgentCommand, "synthetic execute-only action", boundary)
	plan.CurrentAuthority = func(ctx context.Context) error { return lease.ValidateLaunch(ctx, boundary, identity) }
	cmd, cleanup, err := PrepareCommand(t.Context(), entry, []string{"-test.run=^TestExecuteOnlyNativePayload$", "literal value"}, ExecOpts{Launch: plan, Dir: root, Env: []string{"PATH=/usr/bin:/bin", "PW_EXECUTE_ONLY_NATIVE_PAYLOAD=1"}})
	testutil.FailErr(t, "capture without source read", err)
	defer cleanup()
	var output bytes.Buffer
	cmd.Stdout = &output
	testutil.FailErr(t, "run execute-only native image", RunPrepared(cmd))
	if output.String() != "literal value\n" {
		t.Fatal("execute-only invocation changed")
	}
}

func TestPreparedLaunchPreservesRelativeScriptKernelFilename(t *testing.T) {
	for _, nativePath := range []string{"./fixture", "sub/fixture"} {
		t.Run(nativePath, func(t *testing.T) {
			root := t.TempDir()
			entry := filepath.Join(root, nativePath)
			testutil.FailErr(t, "create native script directory", os.MkdirAll(filepath.Dir(entry), 0700))
			testutil.FailErr(t, "write native filename script", os.WriteFile(entry, []byte("#!/bin/sh\nprintf '%s' \"$0\""), 0700))
			cmd, cleanup, err := PrepareCommand(t.Context(), nativePath, nil, ExecOpts{Launch: HostLaunch("synthetic native script filename"), Dir: root})
			testutil.FailErr(t, "prepare literal script filename", err)
			defer cleanup()
			var output bytes.Buffer
			cmd.Stdout = &output
			testutil.FailErr(t, "run literal script filename", RunPrepared(cmd))
			if output.String() != nativePath {
				t.Fatalf("kernel script spelling changed: %q", output.String())
			}
		})
	}
}

func TestExecuteOnlyNativePayload(t *testing.T) {
	if os.Getenv("PW_EXECUTE_ONLY_NATIVE_PAYLOAD") != "1" {
		return
	}
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	fmt.Println(os.Args[2])
	os.Exit(0)
}

func TestPreparedStrictBoundaryNeverFallsBackToHostExecution(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "forbidden-effect")
	boundary := &confine.Confinement{Roots: []string{root}, Network: confine.NetworkDeny}
	plan := AgentLaunch(LaunchAgentCommand, "synthetic strict native boundary refusal", boundary).WithPathAuthority(confine.NativeLaunchPaths(root))
	cmd, cleanup, err := PrepareCommand(t.Context(), "/bin/sh", []string{"-c", "printf forbidden > \"$1\"", "fixture", outside}, ExecOpts{Launch: plan, Dir: root, Env: []string{"PATH=/usr/bin:/bin"}})
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		err = RunPrepared(cmd)
	}
	if err == nil {
		t.Fatal("strict boundary rejection reported native success")
	}
	if _, statErr := os.Stat(outside); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("strict boundary rejection fell back and committed payload")
	}
}
