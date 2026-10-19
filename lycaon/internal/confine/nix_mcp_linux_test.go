//go:build linux && nixworkflow

package confine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxNixExplicitMCPToolchainRetainsProtocolAndEnvironment(t *testing.T) {
	fixture := prepareNixWorkflows(t)
	_, err := confine.StartEgressBroker(t.TempDir())
	testutil.FailErr(t, "start required MCP action broker", err)
	t.Cleanup(func() { _ = confine.StopEgressBroker() })
	peer := filepath.Join(t.TempDir(), "mcp-peer")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", peer, "../mcp/testdata/fake_stdio_server")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build existing MCP protocol fixture: %v\n%s", err, output)
	}
	facts := filepath.Join(fixture.project, "selected-peer-shell")
	poisoned := filepath.Join(fixture.project, "ambient-hook")
	hook := filepath.Join(fixture.project, "bash-env.sh")
	testutil.FailErr(t, "write inherited injection control", os.WriteFile(hook, []byte("printf poisoned > '"+poisoned+"'\n"), 0o600))
	env := make(map[string]string)
	for _, entry := range fixture.env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	env["BASH_ENV"], env["PW_NIX_PEER_FACTS"] = hook, facts
	entry := mcp.MCPProviderEntry{ID: "nix-workflow-peer", Enabled: true, Command: fixture.bin, Env: env,
		Args: []string{"--offline", "--store", fixture.store, "shell", filepath.Dir(filepath.Dir(fixture.bash)), "--command", fixture.bash,
			"-c", `printf '%s' "$BASH" > "$PW_NIX_PEER_FACTS"; exec "$1"`, "nix-peer", peer}}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	session, err := (mcp.SDKConnector{}).Connect(ctx, entry, mcp.ConnectOpts{Roots: fixture.boundary.Roots, Lifetime: ctx})
	testutil.FailErr(t, "connect explicitly configured Nix MCP peer", err)
	t.Cleanup(func() { _ = session.Close() })
	tools, err := session.ListTools(ctx)
	testutil.FailErr(t, "list actual MCP tools through selected Nix shell", err)
	if len(tools) == 0 {
		t.Fatal("selected Nix MCP process exposed no protocol tools")
	}
	result, err := session.CallTool(ctx, "echo", map[string]any{"message": "nix-protocol-control"})
	testutil.FailErr(t, "call actual MCP tool through selected Nix shell", err)
	if result == nil || result.IsError || len(result.Content) == 0 {
		t.Fatalf("selected Nix MCP peer failed its protocol control: %+v", result)
	}
	selected, err := os.ReadFile(facts)
	testutil.FailErr(t, "read observed selected MCP shell", err)
	if string(selected) != fixture.bash {
		t.Fatalf("MCP selected shell=%s; want declared store shell=%s", selected, fixture.bash)
	}
	if _, err := os.Stat(poisoned); !os.IsNotExist(err) {
		t.Fatal("ambient shell injection crossed the MCP environment filter")
	}
	testutil.FailErr(t, "close selected Nix MCP lifetime", session.Close())
	if _, err := session.ListTools(ctx); err == nil {
		t.Fatal("closed selected Nix MCP session remained callable")
	}
}
