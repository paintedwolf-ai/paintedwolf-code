package mcp_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
)

type fakeAS struct {
	URL           string
	client        *http.Client
	issuedRefresh string
	codes         map[string]string
	mu            sync.Mutex
	prmPath       string
}

func startFakeAS(t *testing.T) *fakeAS {
	t.Helper()
	f := &fakeAS{codes: map[string]string{}, prmPath: "/.well-known/oauth-protected-resource"}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	f.client = srv.Client()

	mux.HandleFunc(f.prmPath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"resource":              f.URL + "/mcp",
			"authorization_servers": []string{f.URL},
			"scopes_supported":      []string{"mcp"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                           f.URL,
			"authorization_endpoint":           f.URL + "/authorize",
			"token_endpoint":                   f.URL + "/token",
			"registration_endpoint":            f.URL + "/register",
			"code_challenge_methods_supported": []string{"S256"},
			"response_types_supported":         []string{"code"},
			"grant_types_supported":            []string{"authorization_code", "refresh_token"},
		})
	})
	mux.HandleFunc("/register", f.handleRegister)
	mux.HandleFunc("/authorize", f.handleAuthorize)
	mux.HandleFunc("/token", f.handleToken)
	mux.HandleFunc("/mcp", f.handleMCP)
	return f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeAS) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{
		"client_id":                  "dyn-1",
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"redirect_uris":              []string{r.FormValue("redirect_uris")},
	})
}

func (f *fakeAS) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("resource") == "" {
		http.Error(w, "bad authorize", http.StatusBadRequest)
		return
	}
	code := "auth-code-1"
	f.mu.Lock()
	f.codes[code] = q.Get("code_challenge")
	f.mu.Unlock()
	u, _ := url.Parse(q.Get("redirect_uri"))
	qq := u.Query()
	qq.Set("code", code)
	qq.Set("state", q.Get("state"))
	u.RawQuery = qq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (f *fakeAS) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		if r.Form.Get("resource") == "" {
			http.Error(w, "need resource", http.StatusBadRequest)
			return
		}
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		challenge := base64.RawURLEncoding.EncodeToString(sum[:])
		f.mu.Lock()
		want := f.codes[r.Form.Get("code")]
		f.mu.Unlock()
		if want == "" || want != challenge {
			http.Error(w, "bad pkce", http.StatusBadRequest)
			return
		}
		f.issuedRefresh = "refresh-1"
		writeJSON(w, map[string]any{
			"access_token": "access-1", "refresh_token": f.issuedRefresh,
			"token_type": "Bearer", "expires_in": 3600,
		})
	case "refresh_token":
		if r.Form.Get("refresh_token") != f.issuedRefresh {
			http.Error(w, "bad refresh", http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"access_token": "access-2", "refresh_token": f.issuedRefresh,
			"token_type": "Bearer", "expires_in": 3600,
		})
	default:
		http.Error(w, "bad grant", http.StatusBadRequest)
	}
}

func (f *fakeAS) handleMCP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth != "Bearer access-1" && auth != "Bearer access-2" {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+f.URL+f.prmPath+`"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
}

func TestOAuthPKCEAndRefresh(t *testing.T) {
	as := startFakeAS(t)
	store := mcp.NewOAuthTokenStoreAt(t.TempDir() + "/oauth.yaml")
	client := mcp.NewOAuthClient(store, "http://127.0.0.1:8766/mcp/oauth/callback", as.client)
	entry := mcp.MCPProviderEntry{ID: "remote", URL: as.URL + "/mcp"}

	start, err := client.Start(t.Context(), entry, "", nil)
	testutil.FailErr(t, "oauth start", err)
	if !strings.Contains(start.AuthorizeURL, "code_challenge") || !strings.Contains(start.AuthorizeURL, "resource=") {
		t.Fatalf("authorize url missing pkce/resource: %s", start.AuthorizeURL)
	}
	if !strings.Contains(start.AuthorizeURL, "client_id=dyn-1") {
		t.Fatalf("authorize url missing registered client: %s", start.AuthorizeURL)
	}

	noFollow := &http.Client{
		Transport: as.client.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := noFollow.Get(start.AuthorizeURL)
	testutil.FailErr(t, "authorize nofollow", err)
	defer resp.Body.Close()
	u, err := url.Parse(resp.Header.Get("Location"))
	testutil.FailErr(t, "parse redirect", err)
	code, state := u.Query().Get("code"), u.Query().Get("state")
	if code == "" || state != start.State {
		t.Fatalf("redirect code=%q state=%q want=%q", code, state, start.State)
	}

	testutil.FailErr(t, "complete", client.Complete(t.Context(), "remote", "", mcp.OAuthCompleteRequest{Code: code, State: state}))
	if tok := store.AccessToken("remote"); tok != "access-1" {
		t.Fatalf("access=%q", tok)
	}

	rec, ok := store.Get("remote")
	if !ok {
		t.Fatal("missing record")
	}
	rec.Expiry = time.Now().Add(-time.Minute)
	testutil.FailErr(t, "put expired", store.Put("remote", rec))
	tok, err := client.RefreshAccessToken(t.Context(), "remote")
	testutil.FailErr(t, "refresh", err)
	if tok != "access-2" {
		t.Fatalf("refreshed=%q", tok)
	}
}

func TestOAuthStartRequiresRegistration(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"resource":              srv.URL + "/mcp",
			"authorization_servers": []string{srv.URL},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                           srv.URL,
			"authorization_endpoint":           srv.URL + "/authorize",
			"token_endpoint":                   srv.URL + "/token",
			"code_challenge_methods_supported": []string{"S256"},
			"response_types_supported":         []string{"code"},
			"grant_types_supported":            []string{"authorization_code", "refresh_token"},
		})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+srv.URL+`/.well-known/oauth-protected-resource"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})

	store := mcp.NewOAuthTokenStoreAt(t.TempDir() + "/oauth.yaml")
	client := mcp.NewOAuthClient(store, "http://127.0.0.1:8766/mcp/oauth/callback", srv.Client())
	_, err := client.Start(t.Context(), mcp.MCPProviderEntry{ID: "remote", URL: srv.URL + "/mcp"}, "", nil)
	if !errors.Is(err, mcp.ErrOAuthRegistrationRequired) {
		t.Fatalf("err = %v", err)
	}
}
