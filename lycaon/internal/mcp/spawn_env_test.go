package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// ambientCredentials are variables a developer machine commonly exports to the
// sidecar. A local MCP server is third-party code; none of them may cross into
// it at spawn, before any tool call, approval, or egress gate has run.
var ambientCredentials = map[string]string{
	"ANTHROPIC_API_KEY":     "sk-ant-should-not-cross",
	"AWS_SECRET_ACCESS_KEY": "aws-should-not-cross",
	"AWS_SESSION_TOKEN":     "aws-session-should-not-cross",
	"GITHUB_TOKEN":          "gh-should-not-cross",
	"NPM_TOKEN":             "npm-should-not-cross",
	"SSH_AUTH_SOCK":         "/tmp/ssh-should-not-cross.sock",
}

func exportAmbientCredentials(t *testing.T) {
	t.Helper()
	for k, v := range ambientCredentials {
		t.Setenv(k, v)
	}
}

func envValue(env []string, key string) (string, bool) {
	value, found := "", false
	// Last assignment wins, the same as the child process resolves it.
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == key {
			value, found = v, true
		}
	}
	return value, found
}

func assertNoAmbientCredentials(t *testing.T, what string, env []string) {
	t.Helper()
	// A nil environment means the child inherits everything the sidecar holds,
	// so an empty result is a failure rather than a clean one.
	if _, ok := envValue(env, "PATH"); !ok {
		t.Fatalf("%s carries no PATH; an empty environment inherits the sidecar's own", what)
	}
	for key := range ambientCredentials {
		if value, ok := envValue(env, key); ok {
			t.Fatalf("%s handed the MCP server ambient credential %s=%q", what, key, value)
		}
	}
}

func TestSpawnEnvForDropsAmbientCredentials(t *testing.T) {
	exportAmbientCredentials(t)
	assertNoAmbientCredentials(t, "spawnEnvFor", spawnEnvFor(nil, nil).base)
}

// The operator-declared env map gives one server one credential; it reaches the
// child even though ambient credentials do not.
func TestSpawnEnvForKeepsOperatorDeclaredEnv(t *testing.T) {
	exportAmbientCredentials(t)
	env := spawnEnvFor(map[string]string{"GITHUB_TOKEN": "declared-for-this-server"}, nil)
	value, ok := envValue(env.base, "GITHUB_TOKEN")
	if !ok || value != "declared-for-this-server" {
		t.Fatalf("declared env GITHUB_TOKEN = %q (present=%v), want the operator's value", value, ok)
	}
	if _, ok := envValue(env.base, "ANTHROPIC_API_KEY"); ok {
		t.Fatal("declaring one key must not readmit the rest of the ambient environment")
	}
}

// The spawned command is what actually reaches the server, so assert on the
// environment after the exec layer has built it.
func TestPrepareStdioCommandDropsAmbientCredentials(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	exportAmbientCredentials(t)
	spawn, err := prepareStdioCommand(
		context.Background(), "fixture", "/bin/echo", nil,
		spawnEnvFor(nil, nil), []string{t.TempDir()},
	)
	testutil.FailErr(t, "prepare", err)
	defer spawn.cleanup()
	assertNoAmbientCredentials(t, "the spawned MCP command", spawn.cmd.Env)
}

func TestPrepareStdioCommandKeepsOperatorDeclaredEnv(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	exportAmbientCredentials(t)
	spawn, err := prepareStdioCommand(
		context.Background(), "fixture", "/bin/echo", nil,
		spawnEnvFor(map[string]string{"NPM_TOKEN": "declared-for-this-server"}, nil), []string{t.TempDir()},
	)
	testutil.FailErr(t, "prepare", err)
	defer spawn.cleanup()
	value, ok := envValue(spawn.cmd.Env, "NPM_TOKEN")
	if !ok || value != "declared-for-this-server" {
		t.Fatalf("spawned NPM_TOKEN = %q (present=%v), want the operator's declared value", value, ok)
	}
}

// TestPrepareStdioCommandDeliversHostMintedToken covers the one credential the
// host mints rather than the operator declaring it. LYCAON_API_TOKEN is on the
// exec injection blocklist, so the @self entry's token would be stripped if it
// were routed with the declared environment; it reaches its server only through
// the host channel.
func TestPrepareStdioCommandDeliversHostMintedToken(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	spawn, err := prepareStdioCommand(
		context.Background(), "fixture", "/bin/echo", nil,
		spawnEnvFor(nil, []string{"LYCAON_API_TOKEN=self-token"}), []string{t.TempDir()},
	)
	testutil.FailErr(t, "prepare", err)
	defer spawn.cleanup()
	value, ok := envValue(spawn.cmd.Env, "LYCAON_API_TOKEN")
	if !ok || value != "self-token" {
		t.Fatalf("spawned LYCAON_API_TOKEN = %q (present=%v), want the host-minted @self token", value, ok)
	}
}
