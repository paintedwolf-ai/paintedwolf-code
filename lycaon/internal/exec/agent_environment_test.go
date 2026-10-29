package exec

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func environmentOf(entries []string) map[string]string {
	out := map[string]string{}
	for _, entry := range entries {
		key, value, _ := strings.Cut(entry, "=")
		out[key] = value
	}
	return out
}

// Every agent command launch (command, verify, program stages, terminals)
// starts from the reduced environment, whatever the caller passed, so no
// caller can forget to drop what the engine inherited.
func TestAgentCommandLaunchDropsTheEngineEnvironment(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "host-session-token")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-credential")
	t.Setenv("XDG_CACHE_HOME", "/xdg/cache")
	t.Setenv("USER", "person")
	scratch := t.TempDir()
	plan := AgentLaunch(LaunchAgentCommand, "fixture", &confine.Confinement{SessionScratchRoot: scratch, Network: confine.NetworkDeny})
	for name, opts := range map[string]ExecOpts{
		"default":           {Launch: plan},
		"explicit inherit":  {Launch: plan, Env: os.Environ()},
		"inline and staged": {Launch: plan, InlineEnv: map[string]string{"PW_INLINE": "inline"}},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := &exec.Cmd{}
			testutil.FailErr(t, "configure agent environment", configureStageCmdEnv(cmd, opts, map[string]string{"PW_STAGE": "stage"}))
			env := environmentOf(cmd.Env)
			for _, key := range []string{"CLAUDE_CODE_MESSAGING_TOKEN", "AWS_SECRET_ACCESS_KEY"} {
				if _, leaked := env[key]; leaked {
					t.Fatalf("%s reached an agent launch: %v", key, cmd.Env)
				}
			}
			if env["XDG_CACHE_HOME"] != "/xdg/cache" || env["USER"] != "person" || env["TMPDIR"] != scratch || env["PATH"] == "" || env["PW_STAGE"] != "stage" {
				t.Fatalf("agent launch lost plumbing, boundary or stage entries: %v", cmd.Env)
			}
			if opts.InlineEnv != nil && env["PW_INLINE"] != "inline" {
				t.Fatalf("agent launch lost the call's export: %v", cmd.Env)
			}
		})
	}
	host := &exec.Cmd{}
	testutil.FailErr(t, "configure host environment", configureStageCmdEnv(host, ExecOpts{Launch: HostLaunch("maintenance")}, nil))
	if environmentOf(host.Env)["AWS_SECRET_ACCESS_KEY"] != "ambient-credential" {
		t.Fatal("host maintenance launch lost the inherited environment it is defined by")
	}
}

// Discovery (type, which, command -v) builds the same options a launch does.
func TestAgentLaunchOptionsReduceAndRefuseReservedExports(t *testing.T) {
	opts, err := AgentLaunchOptions(LaunchPlan{}, t.TempDir(), []string{"PW_EXPORT=1"}, nil)
	testutil.FailErr(t, "agent launch options", err)
	if !opts.Launch.reduced() || opts.InlineEnv["PW_EXPORT"] != "1" {
		t.Fatalf("lookup options do not match the launch environment: %+v", opts)
	}
	for _, reserved := range []string{"LYCAON_API_TOKEN=x", "PATH=/tmp", "DYLD_INSERT_LIBRARIES=/x", "GIT_DIR=/x"} {
		if _, err := AgentLaunchOptions(LaunchPlan{}, t.TempDir(), []string{reserved}, nil); !errors.Is(err, ErrBlockedEnvKey) {
			t.Errorf("reserved export %s: %v", reserved, err)
		}
	}
	trusted := LaunchPlan{}.WithTrustedEnvironment([]string{"HTTP_PROXY=http://127.0.0.1:18472", "GIT_SSH_COMMAND=/trusted/ssh"})
	if _, err := AgentLaunchOptions(trusted, t.TempDir(), []string{"HTTP_PROXY=http://127.0.0.1:18472", "GIT_SSH_COMMAND=/trusted/ssh"}, nil); err != nil {
		t.Fatalf("trusted host environment rejected: %v", err)
	}
	for _, changed := range []string{"GIT_SSH_COMMAND=/user/ssh"} {
		if _, err := AgentLaunchOptions(trusted, t.TempDir(), []string{changed}, nil); !errors.Is(err, ErrBlockedEnvKey) {
			t.Errorf("changed host entry %s: %v", changed, err)
		}
	}
}

func TestAgentCommandPlanStatesItsReducedEnvironment(t *testing.T) {
	if plan := AgentLaunch(LaunchAgentCommand, "fixture", nil); plan.Environment != EnvironmentReduced {
		t.Fatalf("agent command plan environment %q", plan.Environment)
	}
	if plan := AgentLaunch(LaunchLocalMCP, "fixture", nil); plan.Environment != EnvironmentInherited {
		t.Fatalf("local MCP plan gained a reduced environment it builds itself: %q", plan.Environment)
	}
}
