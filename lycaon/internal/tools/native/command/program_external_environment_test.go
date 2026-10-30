package command_test

import (
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// sandboxOnlyEnvKeys lists the environment keys that confine.ProcessEnvironment
// and confine.GitSSHEnv may inject onto an external child process when proxy
// confinement is active. Aside from these sandbox-injected keys, native `env`
// and `external -- /usr/bin/env` receive the exact same environment from
// ProcessEnvironment.
var sandboxOnlyEnvKeys = map[string]bool{
	"HTTP_PROXY":             true,
	"HTTPS_PROXY":            true,
	"ALL_PROXY":              true,
	"NO_PROXY":               true,
	"http_proxy":             true,
	"https_proxy":            true,
	"all_proxy":              true,
	"no_proxy":               true,
	"GIT_SSH_COMMAND":        true,
	"LYCAON_SOCKS_PROXY":     true,
	"LYCAON_SSH_KNOWN_HOSTS": true,
}

func TestProgramEnvNeverLeaksEngineOrSecretVarsAndMatchesExternalChild(t *testing.T) {
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
	f.Configure = func(tc *tools.ToolContext) {
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

	nativeLines := sortedEnvLines(nativeStdout, false)
	externalLines := sortedEnvLines(externalStdout, true)
	if !slices.Equal(nativeLines, externalLines) {
		t.Fatalf("native env != external env (modulo sandbox vars):\nnative:   %v\nexternal: %v", nativeLines, externalLines)
	}
}

func sortedEnvLines(stdout string, stripSandbox bool) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if stripSandbox && sandboxOnlyEnvKeys[key] {
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
