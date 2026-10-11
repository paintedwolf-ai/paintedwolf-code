package mcp_test

import (
	"context"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestCheckSurfacesRejectedRows(t *testing.T) {
	stageDistro(t, `providers:
  - id: fixture
    url: http://127.0.0.1:9/mcp
    enabled: false
`)
	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir", os.MkdirAll(overlayDir, 0o700))
	testutil.FailErr(t, "write project mcp", os.WriteFile(filepath.Join(overlayDir, "mcp.yaml"), []byte(`providers:
  - id: evil
    command: /bin/evil
`), 0o600))

	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		GlobalOverridePath: globalPath,
		Connector:          &mcp.MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	reg.Tools.SetToolRegistry(tools.NewDefaultRegistry())
	// The project layer is gated on Applies("project_mcp", p); this test exercises the
	// structural RejectedRow path, so open the gate explicitly.
	reg.Catalog.SetProjectOverlayGate(func(context.Context, string) bool { return true })
	testutil.FailErr(t, "Load", reg.Catalog.Load(context.Background()))

	scope := mcp.ProjectScope("proj-1", projectDir, []string{projectDir})
	rows := reg.Administration.Check(context.Background(), scope)
	found := false
	for _, row := range rows {
		if row.ProviderID == "evil" && row.Status == api.McpCheckRowStatusError && row.Code == mcp.RejectProjectStdioForbidden {
			found = true
		}
	}
	if !found {
		t.Fatalf("check missing rejected evil row: %+v", rows)
	}

	list := reg.Catalog.ListProviders(context.Background(), scope)
	foundList := false
	for _, s := range list {
		if s.ID == "evil" && !s.Enabled && s.LastError == mcp.RejectProjectStdioForbidden {
			if s.Class != "local" {
				t.Fatalf("class=%q want local", s.Class)
			}
			if s.ConnectionSource != string(mcp.CatalogLayerProject) {
				t.Fatalf("connection_source=%q", s.ConnectionSource)
			}
			foundList = true
		}
	}
	if !foundList {
		t.Fatalf("list missing rejected evil: %+v", list)
	}
}

func TestSetProviderEnabledPreservesOverlayURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.yaml")
	testutil.FailErr(t, "seed", os.WriteFile(path, []byte(`providers:
  - id: team
    url: https://intel.example/mcp
    enabled: false
`), 0o600))
	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          dir,
		GlobalOverridePath: path,
		Connector:          &mcp.MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	testutil.FailErr(t, "Load", reg.Catalog.Load(context.Background()))
	testutil.FailErr(t, "enable", reg.Administration.SetProviderEnabled(context.Background(), mcp.CallScope{}, "team", true, ""))
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read", err)
	text := string(data)
	if !strings.Contains(text, "url:") || !strings.Contains(text, "https://intel.example/mcp") {
		t.Fatalf("url not preserved: %s", text)
	}
	if strings.Contains(text, "null") {
		t.Fatalf("unexpected null stubs: %s", text)
	}
	cfg, err := mcp.LoadUserMCPConfig(path)
	testutil.FailErr(t, "reload", err)
	if len(cfg.Providers) != 1 || cfg.Providers[0].Enabled == nil || !*cfg.Providers[0].Enabled {
		t.Fatalf("enabled not set: %+v", cfg.Providers)
	}
	if cfg.Providers[0].URL == nil || *cfg.Providers[0].URL != "https://intel.example/mcp" {
		t.Fatalf("url pointer lost: %+v", cfg.Providers[0])
	}
}

func TestConcurrentProviderUpdatesRetainBothRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.yaml")
	testutil.FailErr(t, "seed", os.WriteFile(path, []byte(`providers:
  - id: first
    url: https://first.example/mcp
    enabled: false
  - id: second
    url: https://second.example/mcp
    enabled: false
`), 0o600))
	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          dir,
		GlobalOverridePath: path,
		Connector:          &mcp.MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	testutil.FailErr(t, "Load", reg.Catalog.Load(t.Context()))
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- reg.Administration.SetProviderEnabled(t.Context(), mcp.CallScope{}, id, true, "")
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for updateErr := range errs {
		testutil.FailErr(t, "concurrent update", updateErr)
	}
	cfg, err := mcp.LoadUserMCPConfig(path)
	testutil.FailErr(t, "reload", err)
	if len(cfg.Providers) != 2 {
		t.Fatalf("provider rows = %d, want 2", len(cfg.Providers))
	}
	for _, provider := range cfg.Providers {
		if provider.Enabled == nil || !*provider.Enabled || provider.URL == nil {
			t.Fatalf("provider update lost fields: %+v", provider)
		}
	}
}

func TestSpawnEnvTokenOnlyForSelf(t *testing.T) {
	stageDistro(t, `providers:
  - id: selfish
    command: "@self"
    args: ["mcp", "scan-explore"]
    enabled: true
  - id: other
    command: /usr/bin/other-mcp
    enabled: true
`)
	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	conn := &capturingConnector{inner: &mcp.MockConnector{}, envs: map[string][]string{}}
	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		GlobalOverridePath: globalPath,
		Connector:          conn,
	})
	testutil.FailErr(t, "NewRuntime", err)
	reg.Tools.SetToolRegistry(tools.NewDefaultRegistry())
	reg.Connections.SetAPIAccess("secret-token")
	testutil.FailErr(t, "Load", reg.Catalog.Load(context.Background()))
	if got := conn.env("selfish"); len(got) != 1 || got[0] != "LYCAON_API_TOKEN=secret-token" {
		t.Fatalf("selfish env=%v", got)
	}
	if got := conn.env("other"); len(got) != 0 {
		t.Fatalf("other must not receive token: %v", got)
	}
}

// discoverAll connects every server on its own goroutine, so the capture map is
// written concurrently — unsynchronized it is a real "concurrent map writes"
// fatal, not a Go race-detector-only report.
type capturingConnector struct {
	inner mcp.SessionConnector

	mu   sync.Mutex
	envs map[string][]string
}

func (c *capturingConnector) Connect(ctx context.Context, entry mcp.MCPProviderEntry, opts mcp.ConnectOpts) (mcp.ProviderSession, error) {
	c.mu.Lock()
	c.envs[entry.ID] = append([]string(nil), opts.ExtraEnv...)
	c.mu.Unlock()
	return c.inner.Connect(ctx, entry, opts)
}

func (c *capturingConnector) env(id string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.envs[id]
}
