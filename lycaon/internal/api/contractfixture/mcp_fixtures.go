package contractfixture

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func AddMcpRecipe(t *testing.T, srv *hostapi.Server, recipeID, projectID string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(wire.CreateMcpProviderRequest{Source: "recipe", RecipeID: recipeID})
	testutil.FailErr(t, "marshal recipe create", err)
	target := "/v1/mcp/providers"
	if projectID != "" {
		target += "?project_id=" + projectID
	}
	req := NewAuthedRequest(http.MethodPost, target, bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func NewLoopbackMCPProvider(t *testing.T) (*hostapi.Server, *mcp.Runtime) {
	t.Helper()
	return NewMCPProviderWithDistro(t, "\n    url: http://127.0.0.1:8765/mcp\n    enabled: false\n", true)
}

// newMCPProviderWithDistro stages one distro row for "svca" from the given YAML body.
// The body decides what a project layer may do with the row: a loopback url is
// enable-able from a project, a command is not. projectMCP opens the project layer.

func NewMCPProvider(t *testing.T, opts ...TestDeps) (*hostapi.Server, *mcp.Runtime) {
	t.Helper()
	return NewMCPProviderWithDistro(t, StdioMCPDistroBody, false, opts...)
}

// newLoopbackMCPProvider builds the same fixture over a loopback HTTP row, which is what a
// project overlay is permitted to enable, with the project MCP layer open.

func NewMCPProviderWithDistro(t *testing.T, distroBody string, projectMCP bool, opts ...TestDeps) (*hostapi.Server, *mcp.Runtime) {
	t.Helper()
	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	toolReg := tools.NewDefaultRegistry()
	StageMCPDistroBody(t, "svca", distroBody)
	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          t.TempDir(),
		GlobalOverridePath: globalPath,
		Connector: &mcp.MockConnector{Tools: map[string][]*sdkmcp.Tool{
			"svca": {
				{Name: "do", Description: "do"},
				{Name: "query", Description: "query"},
			},
		}},
	})
	testutil.FailErr(t, "NewRegistryImpl", err)
	reg.Tools.SetToolRegistry(toolReg)
	if err := reg.Catalog.Load(context.Background()); err != nil {
		testutil.FailErr(t, "reg.Catalog.Load failed", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	store := store.NewMemory()
	deps := hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store, Projects: project.NewMemoryRegistry(),
		Sessions: session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, toolReg)}, External: hostapi.ExternalDependencies{MCP: reg}}
	if projectMCP {
		WithProjectMCP(t, reg)(&deps)
	}
	for _, opt := range opts {
		opt(&deps)
	}
	return hostapi.NewServer(RequiredTestDeps(t, deps), nil, hostapi.TestAPIToken), reg
}

// A project overlay may not switch on a local subprocess. Enabling a stdio server is
// the same escalation as declaring one, and the project layer is refused both.

func PtrBool(v bool) *bool { return &v }

func StageFakeMCPDistro(t *testing.T, id string) {
	t.Helper()
	StageMCPDistroBody(t, id, "\n    command: \"true\"\n    args: []\n    enabled: false\n")
}

// stageMCPDistroBody stages the bundled distro catalog with one server row.

func StageMCPDistroBody(t *testing.T, id, body string) {
	t.Helper()
	configtest.Overlay(t, map[config.Rel]string{
		config.DistroMCP: "providers:\n  - id: " + id + body,
	})
}

const StdioMCPDistroBody = "\n    command: \"true\"\n    args: []\n    enabled: false\n"
