package mcp_test

import (
	"context"
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAddRecipeCopiesDisabledUserOverlay(t *testing.T) {
	reg := newTestRegistry(t, nil, "svca")
	row, err := reg.Administration.AddRecipe(context.Background(), mcp.CallScope{}, "github", "")
	testutil.FailErr(t, "AddRecipe", err)
	if row.ID != "github" || row.Enabled || row.Recipe != "github" || row.Auth != "static_token" {
		t.Fatalf("row = %+v", row)
	}
	if row.Class != api.McpProviderClassWeb {
		t.Fatalf("class = %q", row.Class)
	}
	if row.URL != "https://api.githubcopilot.com/mcp" {
		t.Fatalf("url = %q", row.URL)
	}

	_, err = reg.Administration.AddRecipe(context.Background(), mcp.CallScope{}, "github", "")
	if !mcp.IsAdminCode(err, mcp.RejectDuplicateID) {
		t.Fatalf("duplicate = %v", err)
	}

	_, err = reg.Administration.AddRecipe(context.Background(), mcp.CallScope{}, "not-a-recipe", "")
	if !mcp.IsAdminCode(err, mcp.CodeRecipeNotFound) {
		t.Fatalf("unknown = %v", err)
	}
}

func TestAddRecipeRefusesProjectRemote(t *testing.T) {
	reg := newTestRegistry(t, nil, "svca")
	_, err := reg.Administration.AddRecipe(context.Background(), mcp.CallScope{}, "github", t.TempDir())
	if !mcp.IsAdminCode(err, mcp.RejectProjectRemoteForbidden) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartOAuthRefusesStaticTokenRecipe(t *testing.T) {
	reg := newTestRegistry(t, nil, "svca")
	_, err := reg.Administration.AddRecipe(context.Background(), mcp.CallScope{}, "huggingface", "")
	testutil.FailErr(t, "AddRecipe", err)
	_, err = reg.Credentials.StartOAuth(context.Background(), mcp.CallScope{}, "huggingface")
	if !mcp.IsAdminCode(err, mcp.CodeOAuthNotSupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestAddRecipePagerDutyDeclaresTokenToken(t *testing.T) {
	reg := newTestRegistry(t, nil, "svca")
	row, err := reg.Administration.AddRecipe(context.Background(), mcp.CallScope{}, "pagerduty", "")
	testutil.FailErr(t, "AddRecipe", err)
	if row.CredentialWire != "token_token" {
		t.Fatalf("credential_wire = %q", row.CredentialWire)
	}
	if row.Auth != "static_token" {
		t.Fatalf("auth = %q", row.Auth)
	}
}

func TestConnectStampsRecipeTokenToken(t *testing.T) {
	cap := &entryCaptureConnector{inner: &mcp.MockConnector{Tools: map[string][]*sdkmcp.Tool{
		"pagerduty": {{Name: "list", Description: "list"}},
	}}}
	reg := newTestRegistry(t, cap, "svca")
	_, err := reg.Administration.AddRecipe(context.Background(), mcp.CallScope{}, "pagerduty", "")
	testutil.FailErr(t, "AddRecipe", err)
	secret := "u+secret"
	yes := true
	_, err = reg.Administration.UpdateProvider(context.Background(), mcp.CallScope{}, "pagerduty", api.UpdateMcpProviderRequest{
		Enabled: &yes,
		Token:   &secret,
	}, "")
	testutil.FailErr(t, "enable pagerduty", err)
	_ = reg.Administration.Check(context.Background(), mcp.CallScope{})
	got := cap.entry("pagerduty")
	if got.ID == "" {
		t.Fatal("connect did not see pagerduty")
	}
	if got := mcp.HTTPAuthHeaders(got).Get("Authorization"); got != "Token token=u+secret" {
		t.Fatalf("Authorization=%q", got)
	}
}

type entryCaptureConnector struct {
	inner mcp.SessionConnector
	mu    sync.Mutex
	got   map[string]mcp.MCPProviderEntry
}

func (c *entryCaptureConnector) Connect(ctx context.Context, entry mcp.MCPProviderEntry, opts mcp.ConnectOpts) (mcp.ProviderSession, error) {
	c.mu.Lock()
	if c.got == nil {
		c.got = map[string]mcp.MCPProviderEntry{}
	}
	c.got[entry.ID] = entry
	c.mu.Unlock()
	return c.inner.Connect(ctx, entry, opts)
}

func (c *entryCaptureConnector) entry(id string) mcp.MCPProviderEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.got[id]
}

func TestListRecipesMarksAdded(t *testing.T) {
	reg := newTestRegistry(t, nil, "svca")
	before := reg.Administration.ListRecipes(context.Background(), mcp.CallScope{})
	var found bool
	for _, rec := range before {
		if rec.ID == "github" {
			found = true
			if rec.Added || rec.ProjectOK || rec.Auth != "static_token" || rec.Class != api.McpProviderClassWeb {
				t.Fatalf("github before add = %+v", rec)
			}
		}
	}
	if !found {
		t.Fatal("github recipe missing")
	}
	_, err := reg.Administration.AddRecipe(context.Background(), mcp.CallScope{}, "github", "")
	testutil.FailErr(t, "AddRecipe", err)
	after := reg.Administration.ListRecipes(context.Background(), mcp.CallScope{})
	for _, rec := range after {
		if rec.ID == "github" && !rec.Added {
			t.Fatal("github should be marked added")
		}
	}
}
