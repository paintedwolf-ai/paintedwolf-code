package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// TestConfinedMCPSeamConformance asserts the confined envelope properties on a
// fake stdio server: roots advertised, CallTool output capped, declared code
// bridged into ToolReject + mcp_error_code (via MachineErrorCode).
//
// Every case runs against one registry, in sequence, so a session that survives
// only the call that established it fails the next case.
func TestConfinedMCPSeamConformance(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	root := t.TempDir()
	bin := buildFakeStdioServer(t)
	wantRoot := fileRootURI(mustAbs(t, root))

	reg, err := NewRegistryImpl(RegistryOptions{Connector: SDKConnector{}})
	testutil.FailErr(t, "NewRegistryImpl", err)
	reg.deviceCatalog = []MergedMCPProviderEntry{{
		MCPProviderEntry: MCPProviderEntry{ID: "fixture", Command: bin, Args: legacyRootsFixtureArgs, Enabled: true},
	}}
	reg.SetDeviceProbeRoots(func() []string { return []string{root} })
	t.Cleanup(func() { _ = reg.Close() })

	out, err := reg.CallTool(context.Background(), CallScope{}, "fixture", "list_roots", nil)
	testutil.FailErr(t, "list_roots", err)
	if !strings.Contains(out, wantRoot) {
		t.Fatalf("roots text %q missing %s", out, wantRoot)
	}

	out, err = reg.CallTool(context.Background(), CallScope{}, "fixture", "huge", nil)
	testutil.FailErr(t, "huge", err)
	if !strings.Contains(out, `"truncated":true`) {
		t.Fatalf("expected truncated output, got prefix %q", out[:min(80, len(out))])
	}

	_, err = reg.CallTool(context.Background(), CallScope{}, "fixture", "fail_coded", nil)
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != MCPServerCodePrefix+"FIXTURE_MCP_DENIED" {
		t.Fatalf("err = %v want %sFIXTURE_MCP_DENIED", err, MCPServerCodePrefix)
	}
	if got := oar.MCPMachineErrorCode(err); got != MCPServerCodePrefix+"FIXTURE_MCP_DENIED" {
		t.Fatalf("mcp_error_code = %q", got)
	}

	_, err = reg.CallTool(context.Background(), CallScope{}, "fixture", "fail_plain", nil)
	tr = tools.AsToolReject(err)
	if tr == nil || tr.Code != GenericMCPRejectCode {
		t.Fatalf("err = %v want %s", err, GenericMCPRejectCode)
	}
	if got := oar.MCPMachineErrorCode(err); got != GenericMCPRejectCode {
		t.Fatalf("mcp_error_code = %q", got)
	}
}

// A spawned server must outlive the call that established it: the registry supervises
// the process, not a per-call timeout context. Two calls on one registry is
// the whole test: the second must not land on a dead child.
func TestStdioServerSurvivesTheCallThatSpawnedIt(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	reg := newFixtureRegistry(t, buildFakeStdioServer(t))

	first, err := reg.CallTool(context.Background(), CallScope{}, "fixture", "echo", map[string]any{"message": "one"})
	testutil.FailErr(t, "first call", err)
	if !strings.Contains(first, "one") {
		t.Fatalf("first call = %q", first)
	}
	second, err := reg.CallTool(context.Background(), CallScope{}, "fixture", "echo", map[string]any{"message": "two"})
	testutil.FailErr(t, "second call", err)
	if !strings.Contains(second, "two") {
		t.Fatalf("second call = %q", second)
	}
	// Same session both times — a reconnect would mean the first call killed it.
	if sessions := reg.sessionCount(); sessions != 1 {
		t.Fatalf("registry holds %d sessions, want the one it spawned", sessions)
	}
}

// SyncTools bounds its connect+list with its own per-server deadline. That deadline
// must not follow the process: the session it caches has to still work afterwards.
func TestCallToolAfterSyncToolsUsesLiveSession(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	reg := newFixtureRegistry(t, buildFakeStdioServer(t))
	reg.SetToolRegistry(tools.NewDefaultRegistry())

	testutil.FailErr(t, "sync", reg.SyncTools(context.Background()))
	if got := reg.LastSyncError("fixture"); got != "" {
		t.Fatalf("sync error = %q", got)
	}
	out, err := reg.CallTool(context.Background(), CallScope{}, "fixture", "echo", map[string]any{"message": "after-sync"})
	testutil.FailErr(t, "call after sync", err)
	if !strings.Contains(out, "after-sync") {
		t.Fatalf("call after sync = %q", out)
	}
}

// A session whose transport is gone must be evicted, not cached forever: otherwise
// every later call burns the full timeout against the corpse until the breaker opens.
func TestDeadTransportEvictsCachedSession(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	reg := newFixtureRegistry(t, buildFakeStdioServer(t))

	testutil.FailErr(t, "warm", func() error {
		_, err := reg.CallTool(context.Background(), CallScope{}, "fixture", "echo", map[string]any{"message": "warm"})
		return err
	}())

	// Take the server down behind the registry's back, the way a crashing or
	// externally killed server does — the entry stays in r.sessions.
	dead, ok := reg.sessionFor("fixture").(*sdkSession)
	if !ok {
		t.Fatalf("session type %T", reg.sessionFor("fixture"))
	}
	_ = dead.Close()

	if _, err := reg.CallTool(context.Background(), CallScope{}, "fixture", "echo", map[string]any{"message": "dead"}); err == nil {
		t.Fatal("expected the call against the dead server to fail")
	}
	if reg.sessionFor("fixture") != nil {
		t.Fatal("dead session stayed cached; later calls would fail against a corpse")
	}

	// The next call re-establishes rather than failing forever.
	out, err := reg.CallTool(context.Background(), CallScope{}, "fixture", "echo", map[string]any{"message": "revived"})
	testutil.FailErr(t, "call after eviction", err)
	if !strings.Contains(out, "revived") {
		t.Fatalf("call after eviction = %q", out)
	}
}

// A declared server error is the server working correctly on bad input. It must
// keep its session — evicting there would respawn the server on every tool error.
func TestDeclaredServerErrorKeepsSession(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	reg := newFixtureRegistry(t, buildFakeStdioServer(t))

	for i := 0; i < 3; i++ {
		if _, err := reg.CallTool(context.Background(), CallScope{}, "fixture", "fail_coded", nil); tools.AsToolReject(err) == nil {
			t.Fatalf("call %d: err = %v want ToolReject", i, err)
		}
	}
	if got := reg.sessionCount(); got != 1 {
		t.Fatalf("registry holds %d sessions, want the one it spawned", got)
	}
}

// Close must terminate the child processes, not just drop the sessions.
func TestCloseTerminatesSpawnedServer(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	reg := newFixtureRegistry(t, buildFakeStdioServer(t))
	_, err := reg.CallTool(context.Background(), CallScope{}, "fixture", "echo", map[string]any{"message": "hi"})
	testutil.FailErr(t, "call", err)

	sess, _ := reg.sessionFor("fixture").(*sdkSession)
	if sess == nil || sess.cmd == nil || sess.cmd.Process == nil {
		t.Fatal("no spawned process to reap")
	}

	testutil.FailErr(t, "close", reg.Close())
	if reg.sessionCount() != 0 {
		t.Fatal("Close left sessions cached")
	}
	if sess.cmd.ProcessState == nil {
		t.Fatal("spawned server was not reaped by registry Close")
	}
}

func newFixtureRegistry(t *testing.T, bin string) *RegistryImpl {
	t.Helper()
	reg, err := NewRegistryImpl(RegistryOptions{Connector: SDKConnector{}})
	testutil.FailErr(t, "NewRegistryImpl", err)
	reg.deviceCatalog = []MergedMCPProviderEntry{{
		MCPProviderEntry: MCPProviderEntry{ID: "fixture", Command: bin, Enabled: true},
	}}
	reg.SetDeviceProbeRoots(func() []string { return []string{t.TempDir()} })
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func (r *RegistryImpl) sessionCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}

// sessionFor returns the pooled session for providerID in any scope, or nil.
func (r *RegistryImpl) sessionFor(providerID string) ProviderSession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for ref, pooled := range r.sessions {
		if ref.providerID == providerID {
			return pooled.sess
		}
	}
	return nil
}
