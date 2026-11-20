package mcptest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
)

func OpenOAuthStore(t testing.TB, path string) *mcp.OAuthTokenStore {
	t.Helper()
	store, err := mcp.OpenOAuthTokenStore(credentialstore.Slot{Path: path, Namespace: credentialstore.NamespaceMCPOAuth, Context: "OAuth fixture"})
	testutil.FailErr(t, "open OAuth credential fixture", err)
	return store
}
