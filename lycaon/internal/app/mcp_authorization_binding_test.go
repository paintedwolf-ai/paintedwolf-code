package app

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestBuildSealsAcquiredMCPInventoryAndLiveChanges(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-mcp-inventory")
	cfg := testBuildConfig(t, configlayout.FindModuleRoot())
	configtest.Overlay(t, map[config.Rel]string{config.DistroMCP: "providers:\n  - id: svca\n    command: \"true\"\n    args: []\n    enabled: true\n"})
	cfg.TestLLMClient = llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".*", Text: "Hello."}}})
	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "build acquired MCP graph", err)
	t.Cleanup(func() { _ = app.Close() })
	testdbseed.InsertProjectRoot(t, app.DB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := app.Sessions.Manager.CreateForProject(t.Context(), testdbseed.DefaultProjectID, wire.SessionPostureBuild)
	testutil.FailErr(t, "create inventory session", err)
	handle, ok := app.DB.(db.Handle)
	if !ok {
		t.Fatal("assembled store does not support authorization writes")
	}
	contexts := authzcontext.NewSQLStore(handle)
	seal := func() *authzcontext.Context {
		t.Helper()
		_, err := app.Sessions.Manager.Prompt(t.Context(), sess.ID, "Say hello.")
		testutil.FailErr(t, "run production sealing path", err)
		row, err := contexts.LatestContext(t.Context(), sess.ID)
		testutil.FailErr(t, "read durable authorization context", err)
		if row == nil {
			t.Fatal("run did not seal an authorization context")
		}
		return row
	}
	first := seal()
	if !reflect.DeepEqual(first.MCPInventory.ProviderIDs, []string{"svca"}) {
		t.Fatalf("initial acquired providers = %v", first.MCPInventory.ProviderIDs)
	}
	req := httptest.NewRequest(http.MethodPatch, "/v1/mcp/providers/svca", strings.NewReader(`{"enabled":false}`))
	req.Header.Set("Authorization", "Bearer test-mcp-inventory")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.Server.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("disable provider: status=%d body=%s", response.Code, response.Body.String())
	}
	second := seal()
	if len(second.MCPInventory.ProviderIDs) != 0 {
		t.Fatalf("disabled providers remain in new seal: %v", second.MCPInventory.ProviderIDs)
	}
	if second.ConfigHash == first.ConfigHash || second.ContextSeq != first.ContextSeq+1 {
		t.Fatalf("inventory drift did not append a new seal: first=%#v second=%#v", first, second)
	}
}
