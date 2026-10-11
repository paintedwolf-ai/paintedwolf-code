package mcp_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
)

func newOAuthDeleteHarness(t *testing.T) (*mcp.Runtime, *mcp.OAuthTokenStore) {
	t.Helper()
	store := mcp.NewOAuthTokenStoreAt(filepath.Join(t.TempDir(), "credential-vault.age"))
	testutil.FailErr(t, "seed oauth token", store.Put("svca", mcp.OAuthTokenRecord{
		AccessToken: "secret-token",
	}))
	if !store.SignedIn("svca") {
		t.Fatal("expected the seeded token to be present before delete")
	}
	stageFakeDistro(t, "svca")
	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          t.TempDir(),
		GlobalOverridePath: filepath.Join(t.TempDir(), "mcp.yaml"),
		Connector:          &mcp.MockConnector{},
		OAuthStore:         store,
	})
	testutil.FailErr(t, "NewRuntime", err)
	testutil.FailErr(t, "Load", reg.Catalog.Load(t.Context()))
	return reg, store
}

// Removing a provider at device scope must also revoke its stored OAuth token.
func TestDeleteOverlayRevokesStoredOAuthToken(t *testing.T) {
	reg, store := newOAuthDeleteHarness(t)
	if err := reg.Administration.DeleteOverlay(context.Background(), "svca", ""); err != nil {
		testutil.FailErr(t, "DeleteOverlay", err)
	}
	if store.SignedIn("svca") {
		t.Fatal("device-scope DeleteOverlay must revoke the provider's stored OAuth token, not just its config row")
	}
}

// Tokens are device-global: deleting only a project override must leave the
// device provider's token intact.
func TestDeleteOverlayProjectScopeKeepsDeviceOAuthToken(t *testing.T) {
	reg, store := newOAuthDeleteHarness(t)
	if err := reg.Administration.DeleteOverlay(context.Background(), "svca", t.TempDir()); err != nil {
		testutil.FailErr(t, "DeleteOverlay project scope", err)
	}
	if !store.SignedIn("svca") {
		t.Fatal("project-scope DeleteOverlay must not revoke the device-level OAuth token")
	}
}
