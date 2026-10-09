//go:build integration

package hostcontracts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/events"
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

func TestHandlePatchMCPProviderEnable(t *testing.T) {
	srv, reg := contractfixture.NewMCPProvider(t)
	enabled := true
	body, err := json.Marshal(wire.UpdateMcpProviderRequest{Enabled: &enabled})
	testutil.FailErr(t, "marshal request", err)
	req := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/mcp/providers/svca", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var row wire.McpProvider
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if !row.Enabled {
		t.Fatal("expected enabled")
	}
	if len(reg.Catalog.RegisteredMCPTools()) == 0 {
		t.Fatal("expected mcp tools registered")
	}
}

func TestHandleMCPCheck(t *testing.T) {
	srv, _ := contractfixture.NewMCPProvider(t)
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/mcp/providers/check", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var check wire.McpCheckResponse
	if err := json.Unmarshal(w.Body.Bytes(), &check); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if len(check.Providers) == 0 {
		t.Fatal("expected check rows")
	}
}

// stageFakeMCPDistro replaces the shipped MCP catalog for one test.

func TestMCPSettingsSSEOnToggle(t *testing.T) {
	hub := events.NewMemoryHub()
	ch, cancel, err := hub.Subscribe(context.Background(), events.Subscription{Project: "", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer cancel()

	toolReg := tools.NewDefaultRegistry()
	contractfixture.StageFakeMCPDistro(t, "svca")
	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		GlobalOverridePath: filepath.Join(t.TempDir(), "mcp.yaml"),
		Connector: &mcp.MockConnector{Tools: map[string][]*sdkmcp.Tool{
			"svca": {{Name: "do", Description: "do"}},
		}},
		OnSettingsChange: func() {
			_ = hub.Publish(context.Background(), wire.EventTopicSettings, events.PublishKey{}, wire.SettingsEvent{
				Area: wire.SettingsAreaMcp, Action: "updated",
			})
		},
	})
	testutil.FailErr(t, "NewRegistryImpl", err)
	reg.Tools.SetToolRegistry(toolReg)
	if err := reg.Catalog.Load(context.Background()); err != nil {
		testutil.FailErr(t, "reg.Catalog.Load failed", err)
	}

	store := store.NewMemory()
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store, Projects: project.NewMemoryRegistry(),
		Sessions: session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, toolReg)}, Host: hostapi.HostDependencies{Events: hub}, External: hostapi.ExternalDependencies{MCP: reg}}), nil, hostapi.TestAPIToken)

	enabled := true
	body, err := json.Marshal(wire.UpdateMcpProviderRequest{Enabled: &enabled})
	testutil.FailErr(t, "marshal request", err)
	req := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/mcp/providers/svca", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	hub.FlushDebounced()
	select {
	case evt := <-ch:
		if evt.Topic != wire.EventTopicSettings {
			t.Fatalf("topic = %q", evt.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("expected settings SSE event")
	}
}

// stdioMCPDistroBody declares "svca" as a local subprocess.

func TestHandlePatchMCPProviderProjectCannotEnableStdio(t *testing.T) {
	srv, reg := contractfixture.NewMCPProviderWithDistro(t, contractfixture.StdioMCPDistroBody, true)
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	proj, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, t.TempDir())
	testutil.FailErr(t, "CreateWithRoot", err)

	enabled := true
	body, err := json.Marshal(wire.UpdateMcpProviderRequest{Enabled: &enabled})
	testutil.FailErr(t, "marshal override", err)
	req := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/mcp/providers/svca?project_id="+proj.ID, bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var row wire.McpProvider
	testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &row))
	if row.Enabled {
		t.Fatalf("project overlay enabled a stdio server: %+v", row)
	}
	if reg.Catalog.ProviderEnabled("svca") {
		t.Fatal("device catalog reports the stdio server enabled")
	}
}

// Deleting a project overlay row returns the provider to its inherited device row.

func TestHandleDeleteMCPProviderProjectOverlayInherits(t *testing.T) {
	// The project MCP layer rides project_mcp; a fresh project is untrusted, so
	// the fixture opens the layer.
	srv, _ := contractfixture.NewLoopbackMCPProvider(t)
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir := t.TempDir()
	proj, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
	testutil.FailErr(t, "CreateWithRoot", err)
	q := "?project_id=" + proj.ID

	enabled := true
	body, err := json.Marshal(wire.UpdateMcpProviderRequest{Enabled: &enabled})
	testutil.FailErr(t, "marshal override", err)
	req := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/mcp/providers/svca"+q, bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("override status = %d body=%s", w.Code, w.Body.String())
	}
	var override wire.McpProvider
	if err := json.Unmarshal(w.Body.Bytes(), &override); err != nil {
		testutil.FailErr(t, "decode override", err)
	}
	if override.Source != "override" || !override.Enabled {
		t.Fatalf("override row = %+v", override)
	}

	inheritReq := contractfixture.NewAuthedRequest(http.MethodDelete, "/v1/mcp/providers/svca"+q, nil)
	inheritW := httptest.NewRecorder()
	srv.ServeHTTP(inheritW, inheritReq)
	if inheritW.Code != http.StatusNoContent {
		t.Fatalf("inherit status = %d body=%s", inheritW.Code, inheritW.Body.String())
	}
	listReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/mcp/providers"+q, nil)
	listW := httptest.NewRecorder()
	srv.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listW.Code, listW.Body.String())
	}
	var list wire.McpProviderListResponse
	testutil.FailErr(t, "decode list", json.Unmarshal(listW.Body.Bytes(), &list))
	var inherited wire.McpProvider
	for _, row := range list.Providers {
		if row.ID == "svca" {
			inherited = row
		}
	}
	if inherited.Source != "default" || inherited.Enabled {
		t.Fatalf("inherited row = %+v", inherited)
	}
}

func TestHandleMCPCreateEnableToolsRefreshDelete(t *testing.T) {
	srv, reg := contractfixture.NewMCPProvider(t)
	secret := "pat-never-echo-on-get"
	createBody, err := json.Marshal(wire.CreateMcpProviderRequest{
		Source: "custom",
		ID:     "local-http",
		URL:    "http://127.0.0.1:8765/mcp",
		Token:  secret,
	})
	testutil.FailErr(t, "marshal create", err)
	createReq := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/mcp/providers", bytes.NewReader(createBody))
	createW := httptest.NewRecorder()
	srv.ServeHTTP(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createW.Code, createW.Body.String())
	}
	if strings.Contains(createW.Body.String(), secret) {
		t.Fatalf("create response leaked token: %s", createW.Body.String())
	}
	var created wire.McpProvider
	testutil.FailErr(t, "decode create", json.Unmarshal(createW.Body.Bytes(), &created))
	if created.Enabled {
		t.Fatalf("create defaults: %+v", created)
	}
	if !created.TokenPresent {
		t.Fatal("expected token_present after token write")
	}

	listReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/mcp/providers", nil)
	listW := httptest.NewRecorder()
	srv.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list status = %d", listW.Code)
	}
	if strings.Contains(listW.Body.String(), secret) {
		t.Fatalf("list leaked token: %s", listW.Body.String())
	}

	enableBody, err := json.Marshal(wire.UpdateMcpProviderRequest{Enabled: contractfixture.PtrBool(true)})
	testutil.FailErr(t, "marshal enable", err)
	enableReq := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/mcp/providers/local-http", bytes.NewReader(enableBody))
	enableW := httptest.NewRecorder()
	srv.ServeHTTP(enableW, enableReq)
	if enableW.Code != http.StatusOK {
		t.Fatalf("enable status = %d body=%s", enableW.Code, enableW.Body.String())
	}

	toolsReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/mcp/providers/local-http/tools", nil)
	toolsW := httptest.NewRecorder()
	srv.ServeHTTP(toolsW, toolsReq)
	if toolsW.Code != http.StatusOK {
		t.Fatalf("tools status = %d body=%s", toolsW.Code, toolsW.Body.String())
	}

	refreshReq := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/mcp/providers/local-http/refresh", nil)
	refreshW := httptest.NewRecorder()
	srv.ServeHTTP(refreshW, refreshReq)
	if refreshW.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", refreshW.Code, refreshW.Body.String())
	}

	delReq := contractfixture.NewAuthedRequest(http.MethodDelete, "/v1/mcp/providers/local-http", nil)
	delW := httptest.NewRecorder()
	srv.ServeHTTP(delW, delReq)
	if delW.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s", delW.Code, delW.Body.String())
	}
	if _, ok := reg.Catalog.GetProvider(context.Background(), mcp.CallScope{}, "local-http"); ok {
		t.Fatal("local-http should be gone after overlay delete")
	}
}

func TestHandleCreateMcpProviderRemoteRequiresHTTPS(t *testing.T) {
	srv, _ := contractfixture.NewMCPProvider(t, contractfixture.WithTestUserNotices(t))

	body, err := json.Marshal(wire.CreateMcpProviderRequest{
		Source: "custom",
		ID:     "coropa",
		URL:    "http://172.17.0.2:8080/mcp",
	})
	testutil.FailErr(t, "marshal create", err)
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/mcp/providers", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp wire.ErrorResponse
	testutil.FailErr(t, "decode error", json.Unmarshal(w.Body.Bytes(), &resp))
	if resp.Code != "remote_requires_https" {
		t.Fatalf("code = %q body=%s", resp.Code, w.Body.String())
	}
	if !strings.Contains(resp.Title, "HTTPS") {
		t.Fatalf("title = %q", resp.Title)
	}
	if strings.Contains(resp.Message, "did not complete") {
		t.Fatal("generic fallback leaked onto a coded MCP refusal")
	}
}

func TestHandleUpdateMcpProviderRemoteRequiresHTTPS(t *testing.T) {
	srv, _ := contractfixture.NewMCPProvider(t, contractfixture.WithTestUserNotices(t))

	createBody, err := json.Marshal(wire.CreateMcpProviderRequest{
		Source: "custom",
		ID:     "coropa",
		URL:    "http://127.0.0.1:8765/mcp",
	})
	testutil.FailErr(t, "marshal create", err)
	createReq := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/mcp/providers", bytes.NewReader(createBody))
	createW := httptest.NewRecorder()
	srv.ServeHTTP(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createW.Code, createW.Body.String())
	}

	remote := "http://172.17.0.2:8080/mcp"
	updateBody, err := json.Marshal(wire.UpdateMcpProviderRequest{URL: &remote})
	testutil.FailErr(t, "marshal update", err)
	updateReq := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/mcp/providers/coropa", bytes.NewReader(updateBody))
	updateW := httptest.NewRecorder()
	srv.ServeHTTP(updateW, updateReq)
	if updateW.Code != http.StatusBadRequest {
		t.Fatalf("update status = %d body=%s", updateW.Code, updateW.Body.String())
	}
	var resp wire.ErrorResponse
	testutil.FailErr(t, "decode error", json.Unmarshal(updateW.Body.Bytes(), &resp))
	if resp.Code != "remote_requires_https" {
		t.Fatalf("code = %q body=%s", resp.Code, updateW.Body.String())
	}
}

func TestHandleCreateMcpProviderNormalizesLoopbackHostPort(t *testing.T) {
	srv, _ := contractfixture.NewMCPProvider(t)

	body, err := json.Marshal(wire.CreateMcpProviderRequest{
		Source: "custom",
		ID:     "coropa",
		URL:    "127.0.0.1:8765",
	})
	testutil.FailErr(t, "marshal create", err)
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/mcp/providers", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var row wire.McpProvider
	testutil.FailErr(t, "decode create", json.Unmarshal(w.Body.Bytes(), &row))
	if row.URL != "http://127.0.0.1:8765" {
		t.Fatalf("url = %q", row.URL)
	}
}

func TestHandleMcpRecipesListAndAdd(t *testing.T) {
	srv, _ := contractfixture.NewMCPProvider(t)

	listReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/mcp/recipes", nil)
	listW := httptest.NewRecorder()
	srv.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listW.Code, listW.Body.String())
	}
	var catalog wire.McpRecipeCatalogResponse
	testutil.FailErr(t, "decode recipes", json.Unmarshal(listW.Body.Bytes(), &catalog))
	var github *wire.McpRecipe
	for i := range catalog.Recipes {
		if catalog.Recipes[i].ID == "github" {
			github = &catalog.Recipes[i]
			break
		}
	}
	if github == nil {
		t.Fatal("github recipe missing")
	}
	if github.Auth != "static_token" || github.Class != "web" || github.Added || github.ProjectOK {
		t.Fatalf("github recipe = %+v", github)
	}

	addW := contractfixture.AddMcpRecipe(t, srv, "github", "")
	if addW.Code != http.StatusCreated {
		t.Fatalf("add status = %d body=%s", addW.Code, addW.Body.String())
	}
	var row wire.McpProvider
	testutil.FailErr(t, "decode add", json.Unmarshal(addW.Body.Bytes(), &row))
	if row.ID != "github" || row.Enabled || row.Recipe != "github" || row.Auth != "static_token" || row.Class != "web" {
		t.Fatalf("added row = %+v", row)
	}
	if row.URL != "https://api.githubcopilot.com/mcp" {
		t.Fatalf("url = %q", row.URL)
	}

	againW := contractfixture.AddMcpRecipe(t, srv, "github", "")
	if againW.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d body=%s", againW.Code, againW.Body.String())
	}

	unknownW := contractfixture.AddMcpRecipe(t, srv, "not-a-recipe", "")
	if unknownW.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d body=%s", unknownW.Code, unknownW.Body.String())
	}

	hfAddW := contractfixture.AddMcpRecipe(t, srv, "huggingface", "")
	if hfAddW.Code != http.StatusCreated {
		t.Fatalf("huggingface add status = %d body=%s", hfAddW.Code, hfAddW.Body.String())
	}

	oauthReq := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/mcp/providers/huggingface/oauth/start", nil)
	oauthW := httptest.NewRecorder()
	srv.ServeHTTP(oauthW, oauthReq)
	if oauthW.Code != http.StatusBadRequest {
		t.Fatalf("oauth start status = %d body=%s", oauthW.Code, oauthW.Body.String())
	}
	var oauthErr wire.ErrorResponse
	testutil.FailErr(t, "decode oauth error", json.Unmarshal(oauthW.Body.Bytes(), &oauthErr))
	if oauthErr.Code != "mcp_oauth_not_supported" {
		t.Fatalf("oauth code = %q body=%s", oauthErr.Code, oauthW.Body.String())
	}

	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	proj, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, t.TempDir())
	testutil.FailErr(t, "CreateWithRoot", err)
	projectW := contractfixture.AddMcpRecipe(t, srv, "github", proj.ID)
	if projectW.Code != http.StatusBadRequest {
		t.Fatalf("project add status = %d body=%s", projectW.Code, projectW.Body.String())
	}
	var projectErr wire.ErrorResponse
	testutil.FailErr(t, "decode project error", json.Unmarshal(projectW.Body.Bytes(), &projectErr))
	if projectErr.Code != "project_remote_forbidden" {
		t.Fatalf("project code = %q body=%s", projectErr.Code, projectW.Body.String())
	}
}

// addMcpRecipe creates a provider from a bundled recipe, in projectID's layer when set.

func TestHandleCancelMCPOAuth(t *testing.T) {
	srv, _ := contractfixture.NewMCPProvider(t)
	for _, tc := range []struct {
		name, provider, body string
		want                 int
	}{
		{"absent attempt is idempotent", "svca", `{"state":"attempt"}`, http.StatusNoContent},
		{"missing state", "svca", `{}`, http.StatusBadRequest},
		{"empty state", "svca", `{"state":" "}`, http.StatusBadRequest},
		{"unknown provider", "missing", `{"state":"attempt"}`, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/mcp/providers/"+tc.provider+"/oauth/cancel", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("cancel status = %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
