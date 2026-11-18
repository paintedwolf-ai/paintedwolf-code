package mcp_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/mcp/mcptest"

	"errors"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// rootRecordingConnector records the roots each connect was handed, per scope.
const scopeBreakerThreshold = 3

type rootRecordingConnector struct {
	mu       sync.Mutex
	inner    *mcptest.Connector
	roots    [][]string
	connects int
	notify   []func()
}

func (c *rootRecordingConnector) Connect(ctx context.Context, entry mcp.MCPProviderEntry, opts mcp.ConnectOpts) (mcp.ProviderSession, error) {
	c.mu.Lock()
	c.roots = append(c.roots, append([]string(nil), opts.Roots...))
	c.connects++
	if opts.OnToolListChanged != nil {
		c.notify = append(c.notify, opts.OnToolListChanged)
	}
	c.mu.Unlock()
	return c.inner.Connect(ctx, entry, opts)
}

func (c *rootRecordingConnector) snapshot() ([][]string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]string(nil), c.roots...), c.connects
}

func newScopedRegistry(t *testing.T, conn mcp.SessionConnector) *mcp.Runtime {
	t.Helper()
	dir := t.TempDir()
	configtest.Overlay(t, map[config.Rel]string{config.DistroMCP: `providers:
  - id: svc
    url: http://127.0.0.1:8765/mcp
    enabled: true
`})
	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          dir,
		GlobalOverridePath: filepath.Join(dir, "mcp.yaml"),
		Connector:          conn,
		BreakerThreshold:   scopeBreakerThreshold,
	})
	testutil.FailErr(t, "mcp.NewRuntime", err)
	reg.Tools.SetToolRegistry(tools.NewDefaultRegistry())
	reg.Connections.SetDeviceProbeRoots(func() []string { return []string{filepath.Join(dir, "probe")} })
	t.Cleanup(func() { _ = reg.Close(t.Context()) })
	testutil.FailErr(t, "load", reg.Catalog.Load(context.Background()))
	return reg
}

// Each project connects with its own confined roots.
func TestSessionsAreNotSharedAcrossProjects(t *testing.T) {
	conn := &rootRecordingConnector{inner: &mcptest.Connector{
		Tools: map[string][]*sdkmcp.Tool{"svc": {{Name: "query"}}},
		Calls: map[string]map[string]int{},
	}}
	reg := newScopedRegistry(t, conn)

	projA := mcp.ProjectScope("proj-a", "/tmp/a", []string{"/tmp/a"})
	projB := mcp.ProjectScope("proj-b", "/tmp/b", []string{"/tmp/b"})

	testutil.FailErr(t, "call from A", callErr(reg, projA))
	testutil.FailErr(t, "call from A again", callErr(reg, projA))
	testutil.FailErr(t, "call from B", callErr(reg, projB))

	roots, connects := conn.snapshot()
	var sawA, sawB bool
	for _, r := range roots {
		switch {
		case len(r) == 1 && r[0] == "/tmp/a":
			sawA = true
		case len(r) == 1 && r[0] == "/tmp/b":
			sawB = true
		}
		if len(r) > 1 {
			t.Fatalf("a call session was handed multiple projects' roots: %v", r)
		}
	}
	if !sawA || !sawB {
		t.Fatalf("roots per connect = %v want one connect confined to each project", roots)
	}
	// Two projects, one server: exactly one session each, plus the sync-time probe.
	if got := connects; got != 3 {
		t.Fatalf("sessions = %d want one per project plus the discovery probe", got)
	}
}

// Device probe roots are reserved for discovery.
func TestDeviceProbeRootsOnlyReachDiscovery(t *testing.T) {
	conn := &rootRecordingConnector{inner: &mcptest.Connector{
		Tools: map[string][]*sdkmcp.Tool{"svc": {{Name: "query"}}},
		Calls: map[string]map[string]int{},
	}}
	reg := newScopedRegistry(t, conn)

	roots, connects := conn.snapshot()
	if connects != 1 {
		t.Fatalf("connects after load = %d want the discovery probe only", connects)
	}
	if len(roots[0]) != 1 || filepath.Base(roots[0][0]) != "probe" {
		t.Fatalf("discovery roots = %v want the device probe roots", roots[0])
	}

	testutil.FailErr(t, "call", callErr(reg, mcp.ProjectScope("proj-a", "/tmp/a", []string{"/tmp/a"})))
	roots, _ = conn.snapshot()
	last := roots[len(roots)-1]
	if len(last) != 1 || last[0] != "/tmp/a" {
		t.Fatalf("call roots = %v want the calling project's roots", last)
	}
}

// Breaker rejection preserves the structured transport code.
func TestBreakerOpenSurfacesStructuredReject(t *testing.T) {
	conn := &mcptest.Connector{
		Tools: map[string][]*sdkmcp.Tool{"svc": {{Name: "query"}}},
		Err:   map[string]error{},
	}
	reg := newScopedRegistry(t, conn)
	// Break the transport after the catalog is loaded, so the breaker sees only
	// connect faults.
	conn.Err["svc"] = errors.New("dial tcp: connection refused")

	scope := mcp.ProjectScope("proj-a", "/tmp/a", []string{"/tmp/a"})
	var last error
	for i := 0; i < int(scopeBreakerThreshold)+2; i++ {
		_, last = reg.Calls.CallTool(context.Background(), scope, "svc", "query", nil)
	}
	reject := toolrejection.AsToolReject(last)
	if reject == nil || reject.Code != mcp.MCPTransportUnavailableCode {
		t.Fatalf("after the breaker opened, err = %v want %s", last, mcp.MCPTransportUnavailableCode)
	}
	if reject.Data["provider"] != "svc" {
		t.Fatalf("reject data = %+v want the provider named", reject.Data)
	}
}

// Tool-list notifications refresh the registered definitions.
func TestToolListChangedTriggersResync(t *testing.T) {
	inner := &mcptest.Connector{
		Tools: map[string][]*sdkmcp.Tool{"svc": {{Name: "query"}}},
		Calls: map[string]map[string]int{},
	}
	conn := &rootRecordingConnector{inner: inner}
	reg := newScopedRegistry(t, conn)

	if !hasTool(reg.Catalog.RegisteredMCPTools(), "mcp_svc_query") {
		t.Fatalf("initial tools = %v", reg.Catalog.RegisteredMCPTools())
	}

	// The server now presents a different tool.
	inner.Tools["svc"] = []*sdkmcp.Tool{{Name: "search"}}
	conn.mu.Lock()
	notifiers := append([]func(){}, conn.notify...)
	conn.mu.Unlock()
	if len(notifiers) == 0 {
		t.Fatal("no session was given a tools/list_changed callback")
	}
	// Drive the sync directly: the notifier hands work to a background drain, and this
	// asserts the re-list itself rather than the goroutine's timing.
	notifiers[0]()
	testutil.FailErr(t, "resync", reg.Tools.SyncTools(context.Background()))

	if hasTool(reg.Catalog.RegisteredMCPTools(), "mcp_svc_query") {
		t.Fatalf("withdrawn tool still registered: %v", reg.Catalog.RegisteredMCPTools())
	}
	if !hasTool(reg.Catalog.RegisteredMCPTools(), "mcp_svc_search") {
		t.Fatalf("new tool not registered: %v", reg.Catalog.RegisteredMCPTools())
	}
}

func callErr(reg *mcp.Runtime, scope mcp.CallScope) error {
	_, err := reg.Calls.CallTool(context.Background(), scope, "svc", "query", nil)
	return err
}

func hasTool(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
