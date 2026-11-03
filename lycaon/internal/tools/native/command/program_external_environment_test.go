package command_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// An external process a program launches sees the reduced agent environment,
// the boundary's entries and the program's exports, never what the engine
// inherited from whatever launched it.
func TestProgramExternalLaunchCarriesOnlyTheAgentEnvironment(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "host-session-token")
	t.Setenv("GITHUB_TOKEN", "ambient-credential")
	t.Setenv("PW_TEST_HOST_ONLY", "host-only")
	t.Setenv("PW_CANARY_SECRET", "canary-not-a-real-secret")
	t.Setenv("XDG_CACHE_HOME", "/xdg/cache")
	f := newProgramFixture(t)
	for _, program := range []string{
		"PW_PROGRAM_EXPORT=exported external -- /usr/bin/env",
		"PW_PROGRAM_EXPORT=exported /bin/bash -c /usr/bin/env",
	} {
		out, err := f.call(t, t.Context(), program)
		testutil.FailErr(t, "run env through the program: "+program, err)
		var outcome struct {
			Stdout string
			Status int
		}
		testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(out), &outcome))
		environment := map[string]string{}
		for _, line := range strings.Split(strings.TrimSuffix(outcome.Stdout, "\n"), "\n") {
			key, value, _ := strings.Cut(line, "=")
			environment[key] = value
		}
		if outcome.Status != 0 || len(environment) < 2 {
			t.Fatalf("%s: env outcome %s", program, out)
		}
		for _, key := range []string{"CLAUDE_CODE_MESSAGING_TOKEN", "GITHUB_TOKEN", "PW_TEST_HOST_ONLY", "PW_CANARY_SECRET"} {
			if value, leaked := environment[key]; leaked {
				t.Errorf("%s: engine environment %s=%q reached the external process", program, key, value)
			}
		}
		for key := range environment {
			if strings.HasPrefix(strings.ToUpper(key), "LYCAON_") && key != "LYCAON_SOCKS_PROXY" && key != "LYCAON_SSH_KNOWN_HOSTS" {
				t.Errorf("%s: engine namespace %s reached the external process", program, key)
			}
		}
		if environment["PW_PROGRAM_EXPORT"] != "exported" || environment["XDG_CACHE_HOME"] != "/xdg/cache" || environment["PATH"] == "" || environment["HOME"] == "" {
			t.Fatalf("%s: agent environment lost exports or plumbing: %v", program, environment)
		}
	}

	out, err := f.call(t, t.Context(), "LYCAON_API_TOKEN=forged external -- /usr/bin/env")
	testutil.FailErr(t, "run host-reserved export", err)
	if outcome := decodeOutcome(t, out); outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_OPERAND_INVALID" {
		t.Fatalf("host-reserved export was not refused: %s", out)
	}
}

func TestProgramJobsStaySessionScopedWhilePsRunsForBothSessions(t *testing.T) {
	shared := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() { _ = shared.Lifecycle.Close(context.Background()) })
	s1 := &programFixture{programtest.New(t, programtest.Options{Session: "session-1", Background: shared})}
	s2 := &programFixture{programtest.New(t, programtest.Options{Session: "session-2", Background: shared})}

	_, err := s1.call(t, t.Context(), "external -- /bin/sleep 30 &")
	testutil.FailErr(t, "launch session-1 background job", err)
	handle := s1.jobHandle(t)
	registryHandle := s1.registryHandle(t)
	defer func() {
		_, _ = s1.call(t, t.Context(), "kill -TERM "+handle)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		_, _, _ = shared.Lifecycle.Wait(ctx, s1.Session, registryHandle)
	}()

	psOut1, err := s1.call(t, t.Context(), "ps")
	testutil.FailErr(t, "run ps in session-1", err)
	if outcome := decodeOutcome(t, psOut1); !strings.HasPrefix(outcome.Stdout, "PID ") {
		t.Fatalf("session-1 ps did not report the process table: %q", outcome.Stdout)
	}

	jobsOut2, err := s2.call(t, t.Context(), "jobs")
	testutil.FailErr(t, "run jobs in session-2", err)
	if outcome := decodeOutcome(t, jobsOut2); outcome.Stdout != "" {
		t.Fatalf("session-2 jobs listed another session's job: %q", outcome.Stdout)
	}
	psOut2, err := s2.call(t, t.Context(), "ps")
	testutil.FailErr(t, "run ps in session-2", err)
	if outcome := decodeOutcome(t, psOut2); !strings.HasPrefix(outcome.Stdout, "PID ") || strings.Contains(outcome.Stdout, "[session job") {
		t.Fatalf("session-2 ps did not report the process table without claiming a job: %q", outcome.Stdout)
	}
}

func TestProgramExternalOutsideWriteRefusedWhileTempWriteAllowed(t *testing.T) {
	if runtime.GOOS != "darwin" || !confine.Available() {
		t.Skip("darwin + seatbelt sandbox required")
	}
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")

	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	testutil.FailErr(t, "look up account home", err)
	stateDir, err := os.MkdirTemp(account.HomeDir, ".paintedwolf-cmd-state-") //nolint:usetesting // /tmp must stay a write root
	testutil.FailErr(t, "create state fixture dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	t.Setenv("HOME", stateDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(stateDir, "xdg-config"))

	outsideDir, err := os.MkdirTemp(account.HomeDir, ".paintedwolf-cmd-outside-") //nolint:usetesting // Must lie outside OS temporary write roots.
	testutil.FailErr(t, "create outside fixture dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(outsideDir) })

	tmpDir, err := os.MkdirTemp("/tmp", "paintedwolf-cmd-temp-") //nolint:usetesting // Exercises /tmp write root explicitly.
	testutil.FailErr(t, "create /tmp fixture dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	f := newProgramFixture(t)
	tmpTarget := filepath.Join(tmpDir, "allowed.txt")
	out, err := f.call(t, t.Context(), "external -- /bin/sh -c 'printf ok > \"$1\"' sh "+tmpTarget)
	testutil.FailErr(t, "run confined /tmp write", err)
	if outcome := decodeOutcome(t, out); outcome.Status != 0 {
		t.Fatalf("confined /tmp write failed: %+v", outcome)
	}
	if data, readErr := os.ReadFile(tmpTarget); readErr != nil || string(data) != "ok" {
		t.Fatalf("/tmp file content = %q, %v", data, readErr)
	}

	outsideTarget := filepath.Join(outsideDir, "refused.txt")
	out, err = f.call(t, t.Context(), "external -- /bin/sh -c 'printf nope > \"$1\"' sh "+outsideTarget)
	testutil.FailErr(t, "run confined outside write", err)
	if outcome := decodeOutcome(t, out); outcome.Status == 0 {
		t.Fatalf("confined outside write unexpectedly succeeded: %+v", outcome)
	}
	if _, statErr := os.Lstat(outsideTarget); !os.IsNotExist(statErr) {
		t.Fatalf("confined outside write created %s: %v", outsideTarget, statErr)
	}
}

func TestProgramExternalMacOSShimToolsWriteDarwinUserTempWithoutStderr(t *testing.T) {
	if runtime.GOOS != "darwin" || !confine.Available() {
		t.Skip("darwin + seatbelt sandbox required")
	}
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	t.Setenv("TMPDIR", t.TempDir())

	f := newProgramFixture(t)
	for _, program := range []string{
		"external -- /usr/bin/git --version",
		"/usr/bin/python3 -c 'print(1)'",
	} {
		out, err := f.call(t, t.Context(), program)
		testutil.FailErr(t, "run "+program, err)
		outcome := decodeOutcome(t, out)
		if outcome.Status != 0 || outcome.Stderr != "" {
			t.Fatalf("%s: status=%d stderr=%q stdout=%q", program, outcome.Status, outcome.Stderr, outcome.Stdout)
		}
	}
}

func TestProgramEnvNeverLeaksEngineOrSecretVarsAndMatchesExternalChild(t *testing.T) {
	restoreBroker := confine.SetBrokerForTest(nil, egressproxy.Addrs{HTTP: "127.0.0.1:18472", SOCKS: "127.0.0.1:18473"})
	t.Cleanup(restoreBroker)
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skip("env binary not found")
	}

	t.Setenv("LYCAON_INTERNAL_SECRET", "engine-internal")
	t.Setenv("PW_CANARY_SECRET", "canary-secret-value")
	t.Setenv("OPENAI_API_KEY", "sk-openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic-secret")
	t.Setenv("GITHUB_TOKEN", "ghp-secret-token")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret-key")
	t.Setenv("USER", "paintedwolf")
	t.Setenv("LANG", "en_US.UTF-8")

	f := newProgramFixture(t)
	scratch := t.TempDir()
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Host.SessionScratchDir = scratch
	}

	decodeStdout := func(raw string) string {
		t.Helper()
		var out command.ProgramOutcome
		testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(raw), &out))
		if out.Status != 0 {
			t.Fatalf("program exited %d: stderr=%q refusal=%+v", out.Status, out.Stderr, out.Refusal)
		}
		return out.Stdout
	}

	nativeRaw, err := f.call(t, t.Context(), "export PROGRAM_VAR=exported_value; env")
	testutil.FailErr(t, "run native env", err)
	nativeStdout := decodeStdout(nativeRaw)

	externalRaw, err := f.call(t, t.Context(), "export PROGRAM_VAR=exported_value; external -- "+envBin)
	testutil.FailErr(t, "run external env", err)
	externalStdout := decodeStdout(externalRaw)

	for _, forbidden := range []string{
		"LYCAON_INTERNAL_SECRET",
		"PW_CANARY_SECRET",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"GITHUB_TOKEN",
		"AWS_SECRET_ACCESS_KEY",
	} {
		if strings.Contains(nativeStdout, forbidden) {
			t.Fatalf("native env leaked %s:\n%s", forbidden, nativeStdout)
		}
		if strings.Contains(externalStdout, forbidden) {
			t.Fatalf("external env leaked %s:\n%s", forbidden, externalStdout)
		}
	}

	for _, required := range []string{
		"PATH=",
		"USER=paintedwolf",
		"LANG=en_US.UTF-8",
		"TMPDIR=",
		"SCRATCH_DIR=",
		"PROGRAM_VAR=exported_value",
	} {
		if !strings.Contains(nativeStdout, required) {
			t.Fatalf("native env missing %s:\n%s", required, nativeStdout)
		}
		if !strings.Contains(externalStdout, required) {
			t.Fatalf("external env missing %s:\n%s", required, externalStdout)
		}
	}

	nativeLines := sortedEnvLines(nativeStdout)
	externalLines := sortedEnvLines(externalStdout)
	if !slices.Equal(nativeLines, externalLines) {
		t.Fatalf("native env != external env:\nnative:   %v\nexternal: %v", nativeLines, externalLines)
	}
	for _, name := range []string{"PATH", "HTTP_PROXY", "GIT_SSH_COMMAND", "LYCAON_SOCKS_PROXY"} {
		out, err := f.call(t, t.Context(), "printenv "+name)
		testutil.FailErr(t, "run native printenv "+name, err)
		got := decodeOutcome(t, out).Stdout
		for _, line := range nativeLines {
			if key, value, ok := strings.Cut(line, "="); ok && key == name && got != value+"\n" {
				t.Fatalf("printenv %s = %q, want %q", name, got, value+"\n")
			}
		}
	}
	for _, line := range nativeLines {
		if key, value, ok := strings.Cut(line, "="); ok && key == "HTTP_PROXY" {
			out, err := f.call(t, t.Context(), `printf '%s\n' "$HTTP_PROXY"`)
			testutil.FailErr(t, "expand HTTP_PROXY", err)
			if got := decodeOutcome(t, out).Stdout; got != value+"\n" {
				t.Fatalf("$HTTP_PROXY = %q, want %q", got, value+"\n")
			}
		}
	}
}

func sortedEnvLines(stdout string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	slices.Sort(out)
	return out
}

func TestProgramExternalRefusesModifiedPath(t *testing.T) {
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skip("env binary not found")
	}
	f := programtest.New(t, programtest.Options{})
	out, err := f.Call(t, t.Context(), "export PATH=/tmp; external -- "+envBin)
	if err == nil && !strings.Contains(out, "COMMAND_OPERAND_INVALID") {
		t.Fatalf("expected modified PATH to be refused, got out=%q err=%v", out, err)
	}
}
