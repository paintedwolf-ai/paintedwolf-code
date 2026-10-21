package command_test

import (
	"context"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
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

func TestProgramSessionJobsAndPsScopeToTheSession(t *testing.T) {
	shared := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() { _ = shared.Lifecycle.Close(context.Background()) })
	s1 := &programFixture{programtest.New(t, programtest.Options{Session: "session-1", Background: shared})}
	s2 := &programFixture{programtest.New(t, programtest.Options{Session: "session-2", Background: shared})}

	_, err := s1.call(t, t.Context(), "external -- /bin/sleep 30 &")
	testutil.FailErr(t, "launch session-1 background job", err)
	handle := s1.jobHandle(t)
	defer func() {
		_, _ = s1.call(t, t.Context(), "kill -TERM "+handle)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		_, _, _ = shared.Lifecycle.Wait(ctx, s1.Session, handle)
	}()

	psOut1, err := s1.call(t, t.Context(), "ps")
	testutil.FailErr(t, "run ps in session-1", err)
	if outcome := decodeOutcome(t, psOut1); !strings.Contains(outcome.Stdout, handle) {
		t.Fatalf("session-1 ps did not list its own job %s: %q", handle, outcome.Stdout)
	}

	for _, cmd := range []string{"jobs", "ps"} {
		out2, err := s2.call(t, t.Context(), cmd)
		testutil.FailErr(t, "run "+cmd+" in session-2", err)
		if outcome := decodeOutcome(t, out2); outcome.Stdout != "" {
			t.Fatalf("session-2 %s listed another session's job: %q", cmd, outcome.Stdout)
		}
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
