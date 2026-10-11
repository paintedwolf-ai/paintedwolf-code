package mcp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// overlayFixture builds a registry over a distro catalog, a user overlay, and one
// project overlay.
type overlayFixture struct {
	reg        *mcp.Runtime
	projectDir string
	calls      map[string]map[string]int
	applies    *bool
	connector  *overlayRecordingConnector
}

type overlayRecordingConnector struct {
	inner *mcp.MockConnector
	mu    sync.Mutex
	seen  []mcp.MCPProviderEntry
}

func (c *overlayRecordingConnector) Connect(ctx context.Context, entry mcp.MCPProviderEntry, opts mcp.ConnectOpts) (mcp.ProviderSession, error) {
	c.mu.Lock()
	c.seen = append(c.seen, entry)
	c.mu.Unlock()
	return c.inner.Connect(ctx, entry, opts)
}

func (c *overlayRecordingConnector) last(providerID string) (mcp.MCPProviderEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.seen) - 1; i >= 0; i-- {
		if c.seen[i].ID == providerID {
			return c.seen[i], true
		}
	}
	return mcp.MCPProviderEntry{}, false
}

func newOverlayFixture(t *testing.T, distro, user, project string, providerTools map[string][]*sdkmcp.Tool) *overlayFixture {
	t.Helper()
	stageDistro(t, distro)
	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	testutil.FailErr(t, "write user", os.WriteFile(globalPath, []byte(user), 0o600))

	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir", os.MkdirAll(overlayDir, 0o700))
	testutil.FailErr(t, "write project mcp",
		os.WriteFile(filepath.Join(overlayDir, "mcp.yaml"), []byte(project), 0o600))

	applies := true
	calls := map[string]map[string]int{}
	connector := &overlayRecordingConnector{inner: &mcp.MockConnector{Tools: providerTools, Calls: calls}}
	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          t.TempDir(),
		GlobalOverridePath: globalPath,
		Connector:          connector,
	})
	testutil.FailErr(t, "NewRuntime", err)
	reg.Tools.SetToolRegistry(tools.NewDefaultRegistry())
	reg.Catalog.SetProjectOverlayGate(func(context.Context, string) bool { return applies })
	t.Cleanup(func() { _ = reg.Close(t.Context()) })
	testutil.FailErr(t, "load", reg.Catalog.Load(context.Background()))
	return &overlayFixture{reg: reg, projectDir: projectDir, calls: calls, applies: &applies, connector: connector}
}

func (f *overlayFixture) scope() mcp.CallScope {
	return mcp.ProjectScope("proj-1", f.projectDir, []string{f.projectDir})
}

// Project-declared providers remain visible but never register agent tools.
func TestProjectDeclaredProviderNeverRegistersATool(t *testing.T) {
	f := newOverlayFixture(t,
		"providers: []\n",
		"providers: []\n",
		`providers:
  - id: proj-svc
    url: http://127.0.0.1:8765/mcp
    enabled: true
`,
		map[string][]*sdkmcp.Tool{"proj-svc": {{Name: "query", Description: "query"}}},
	)

	if containsTool(f.reg.Catalog.RegisteredMCPTools(), "mcp_proj_svc_query") {
		t.Fatalf("project-declared provider registered a host tool: %v", f.reg.Catalog.RegisteredMCPTools())
	}
	if _, err := f.reg.Calls.CallTool(context.Background(), f.scope(), "proj-svc", "query", nil); err == nil {
		t.Fatal("calling a project-declared provider must fail closed")
	}
	if f.calls["proj-svc"]["query"] != 0 {
		t.Fatalf("calls=%v want the provider never invoked", f.calls)
	}

	// The project can inspect and adopt the provider at device scope.
	var found bool
	for _, row := range f.reg.Catalog.ListProviders(context.Background(), f.scope()) {
		if row.ID == "proj-svc" {
			found = true
			if row.ConnectionSource != string(mcp.CatalogLayerProject) {
				t.Fatalf("connection_source = %q", row.ConnectionSource)
			}
		}
	}
	if !found {
		t.Fatal("project-declared provider missing from the project's Settings view")
	}

	// Inspection does not grant agent access.
	infos, err := f.reg.Administration.ListDiscoveredTools(context.Background(), f.scope(), "proj-svc")
	testutil.FailErr(t, "browse project-declared tools", err)
	if len(infos) != 1 || infos[0].Name != "mcp_proj_svc_query" {
		t.Fatalf("tool browser = %+v", infos)
	}
}

// The project layer merges only while the project overlay gate applies.
func TestClosedGateProjectOverlayIsNotMerged(t *testing.T) {
	f := newOverlayFixture(t,
		"providers: []\n",
		"providers: []\n",
		`providers:
  - id: proj-svc
    url: http://127.0.0.1:8765/mcp
    enabled: true
`,
		nil,
	)
	*f.applies = false
	for _, row := range f.reg.Catalog.ListProviders(context.Background(), f.scope()) {
		if row.ID == "proj-svc" {
			t.Fatal("project overlay behind a closed gate was merged into the view")
		}
	}
}

// Project disablement applies only within that project.
func TestProjectOverlayDisableIsScopedToThatProject(t *testing.T) {
	f := newOverlayFixture(t,
		"providers: []\n",
		`providers:
  - id: svc
    url: http://127.0.0.1:8765/mcp
    enabled: true
`,
		`providers:
  - id: svc
    enabled: false
`,
		map[string][]*sdkmcp.Tool{"svc": {{Name: "query", Description: "query"}}},
	)

	if !containsTool(f.reg.Catalog.RegisteredMCPTools(), "mcp_svc_query") {
		t.Fatalf("device provider must still register: %v", f.reg.Catalog.RegisteredMCPTools())
	}
	_, err := f.reg.Calls.CallTool(context.Background(), f.scope(), "svc", "query", nil)
	if err == nil || !strings.Contains(err.Error(), "disabled for this project") {
		t.Fatalf("call from the disabling project = %v, want refused", err)
	}

	other := mcp.ProjectScope("proj-2", t.TempDir(), []string{t.TempDir()})
	if _, err := f.reg.Calls.CallTool(context.Background(), other, "svc", "query", nil); err != nil {
		testutil.FailErr(t, "call from another project", err)
	}
}

// A project cannot enable a credentialed provider.
func TestProjectCannotEnableCredentialedProvider(t *testing.T) {
	f := newOverlayFixture(t,
		"providers: []\n",
		`providers:
  - id: corp
    url: https://corp.example.com/mcp
    token: "USER-TOKEN"
    enabled: false
`,
		`providers:
  - id: corp
    enabled: true
`,
		nil,
	)

	rows := f.reg.Catalog.ListProviders(context.Background(), f.scope())
	var enabled, rejected bool
	for _, row := range rows {
		if row.ID != "corp" {
			continue
		}
		if row.Status == api.McpStatusRejected {
			rejected = true
			if row.LastError != mcp.RejectProjectEnableForbidden {
				t.Fatalf("reject reason = %q", row.LastError)
			}
			continue
		}
		enabled = row.Enabled
	}
	if enabled {
		t.Fatal("project overlay enabled a credentialed provider")
	}
	if !rejected {
		t.Fatalf("expected a project_enable_forbidden row, got %+v", rows)
	}
	if f.reg.Catalog.ProviderEnabled("corp") {
		t.Fatal("device catalog reports the provider enabled")
	}
}

// A project may enable what it could have declared itself: loopback and no credentials.
func TestProjectMayEnableLoopbackProvider(t *testing.T) {
	f := newOverlayFixture(t,
		`providers:
  - id: local
    url: http://127.0.0.1:8765/mcp
    enabled: false
`,
		"providers: []\n",
		`providers:
  - id: local
    enabled: true
`,
		nil,
	)
	for _, row := range f.reg.Catalog.ListProviders(context.Background(), f.scope()) {
		if row.ID == "local" && !row.Enabled {
			t.Fatalf("project enable of a bare loopback row was refused: %+v", row)
		}
	}
	if _, err := f.reg.Calls.CallTool(context.Background(), f.scope(), "local", "query", nil); err != nil {
		testutil.FailErr(t, "call project-enabled loopback provider", err)
	}
	if f.calls["local"]["query"] != 1 {
		t.Fatalf("project-enabled calls = %v, want one query", f.calls)
	}
}

func TestProjectConnectionOverrideIsUsedForInvocation(t *testing.T) {
	f := newOverlayFixture(t,
		"providers: []\n",
		`providers:
  - id: local
    url: http://127.0.0.1:8765/mcp
    enabled: true
`,
		`providers:
  - id: local
    url: http://127.0.0.1:9876/project-mcp
`,
		map[string][]*sdkmcp.Tool{"local": {{Name: "query"}}},
	)
	if _, err := f.reg.Calls.CallTool(context.Background(), f.scope(), "local", "query", nil); err != nil {
		testutil.FailErr(t, "call project-overridden provider", err)
	}
	entry, ok := f.connector.last("local")
	if !ok || entry.URL != "http://127.0.0.1:9876/project-mcp" {
		t.Fatalf("invocation connection = %+v, found=%v", entry, ok)
	}
	projectPath := filepath.Join(f.projectDir, settingsoverlay.DirName(), "mcp.yaml")
	testutil.FailErr(t, "change project connection override", os.WriteFile(projectPath, []byte(`providers:
  - id: local
    url: http://127.0.0.1:9988/reloaded
`), 0o600))
	if _, err := f.reg.Calls.CallTool(context.Background(), f.scope(), "local", "query", nil); err != nil {
		testutil.FailErr(t, "call reloaded project override", err)
	}
	entry, ok = f.connector.last("local")
	if !ok || entry.URL != "http://127.0.0.1:9988/reloaded" {
		t.Fatalf("reloaded invocation connection = %+v, found=%v", entry, ok)
	}
}

func containsTool(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
