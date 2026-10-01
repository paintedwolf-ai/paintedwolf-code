package mcp_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
)

func seedExpiredGrant(t *testing.T, tokenURL string) *mcp.OAuthTokenStore {
	t.Helper()
	store := mcp.NewOAuthTokenStoreAt(t.TempDir() + "/oauth.yaml")
	testutil.FailErr(t, "seed token", store.Put("remote", mcp.OAuthTokenRecord{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Minute),
		Resource:     "https://remote.example/mcp",
		ClientID:     "test-client",
		TokenURL:     tokenURL,
	}))
	return store
}

func TestRefreshKeepsGrantOnTransientFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer srv.Close()

	store := seedExpiredGrant(t, srv.URL+"/token")
	client := mcp.NewOAuthClient(store, "http://127.0.0.1:8766/mcp/oauth/callback", srv.Client())

	if _, err := client.RefreshAccessToken(t.Context(), "remote"); err == nil {
		t.Fatal("refresh against a 502 must fail")
	}
	if !store.SignedIn("remote") {
		t.Fatal("transient refresh failure must not sign the user out")
	}
	rec, ok := store.Get("remote")
	if !ok || rec.RefreshToken != "refresh-1" {
		t.Fatalf("refresh token not preserved: %+v", rec)
	}
}

func TestRefreshDropsGrantOnInvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()

	store := seedExpiredGrant(t, srv.URL+"/token")
	client := mcp.NewOAuthClient(store, "http://127.0.0.1:8766/mcp/oauth/callback", srv.Client())

	if _, err := client.RefreshAccessToken(t.Context(), "remote"); err == nil {
		t.Fatal("refresh against invalid_grant must fail")
	}
	if store.SignedIn("remote") {
		t.Fatal("a revoked refresh token must clear the stored grant")
	}
}

func TestRefreshDropsGrantOnUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	store := seedExpiredGrant(t, srv.URL+"/token")
	client := mcp.NewOAuthClient(store, "http://127.0.0.1:8766/mcp/oauth/callback", srv.Client())

	if _, err := client.RefreshAccessToken(t.Context(), "remote"); err == nil {
		t.Fatal("refresh against a 401 must fail")
	}
	if store.SignedIn("remote") {
		t.Fatal("a 401 from the token endpoint must clear the stored grant")
	}
}
