package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/httpclient"
)

// ErrOAuthRegistrationRequired is returned when the authorization server does
// not advertise a usable dynamic client registration endpoint.
var ErrOAuthRegistrationRequired = errors.New("authorization server does not accept dynamic client registration")

// errReauthRequired marks a refresh path that cannot proceed without a fresh
// user authorization (no grant on file, or nothing to refresh with).
var errReauthRequired = errors.New("reauthorization required")

// tokenEndpointError is a non-2xx token-endpoint response. Code carries the
// RFC 6749 §5.2 error code when the body supplies one.
type tokenEndpointError struct {
	Status int
	Code   string
}

func (e *tokenEndpointError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("token endpoint: HTTP %d: %s", e.Status, e.Code)
	}
	return fmt.Sprintf("token endpoint: HTTP %d", e.Status)
}

// authFailure reports whether the authorization server rejected the credential
// itself, as opposed to failing transiently. Anything else — a 5xx, a proxy
// error, a network fault — leaves the stored grant intact.
func (e *tokenEndpointError) authFailure() bool {
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden {
		return true
	}
	switch e.Code {
	case "invalid_grant", "invalid_client", "unauthorized_client":
		return true
	}
	return false
}

// isOAuthAuthFailure reports whether err means the user must authorize again,
// as opposed to a transient failure worth retrying with the tokens on file.
func isOAuthAuthFailure(err error) bool {
	if errors.Is(err, errReauthRequired) {
		return true
	}
	var te *tokenEndpointError
	if errors.As(err, &te) {
		return te.authFailure()
	}
	return false
}

// OAuthStartResult is returned by StartOAuth for Den / wire.
type OAuthStartResult struct {
	AuthorizeURL string
	State        string
	RedirectURI  string
}

// OAuthCompleteRequest finishes the authorization-code exchange.
type OAuthCompleteRequest struct {
	Code  string
	State string
}

// pendingOAuthTTL bounds how long an unfinished authorization's PKCE verifier
// and state stay redeemable: long enough for a human to finish browser consent.
const pendingOAuthTTL = 10 * time.Minute

type pendingOAuth struct {
	ProviderID   string
	ProjectID    string
	State        string
	Verifier     string
	RedirectURI  string
	Resource     string
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scopes       []string
	CreatedAt    time.Time
	// listener is the loopback server waiting for this authorization's redirect.
	listener *callbackListener
}

func (p pendingOAuth) expired(now time.Time) bool {
	return now.Sub(p.CreatedAt) > pendingOAuthTTL
}

// OAuthClient runs MCP Authorization (OAuth 2.1 + PKCE) for remote HTTP MCP providers.
type OAuthClient struct {
	mu      sync.Mutex
	store   *OAuthTokenStore
	http    *http.Client
	pending map[string]pendingOAuth // providerID -> pending
	// redirectOverride pins the redirect URI instead of binding a loopback listener.
	// Tests set it; the app does not.
	redirectOverride string
	// now is the clock, injectable so expiry is testable without sleeping.
	now func() time.Time
}

// NewOAuthClient constructs an OAuth client.
//
// redirectURI is normally empty: each authorization binds its own ephemeral loopback
// listener and advertises that. A non-empty value pins the redirect for tests.
func NewOAuthClient(store *OAuthTokenStore, redirectURI string, httpClient *http.Client) *OAuthClient {
	if httpClient == nil {
		httpClient = httpclient.Bounded(egressclass.MCPRemoteHTTP, 30*time.Second)
	}
	return &OAuthClient{
		store:            store,
		http:             httpClient,
		pending:          map[string]pendingOAuth{},
		redirectOverride: strings.TrimSpace(redirectURI),
		now:              time.Now,
	}
}

func (c *OAuthClient) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// Start begins authorization for entry.URL: probe 401/PRM, discover AS, bind the
// loopback listener, and build the PKCE authorize URL.
//
// onComplete fires from the loopback listener once the code exchange finishes, with a
// nil error on success.
func (c *OAuthClient) Start(ctx context.Context, entry MCPProviderEntry, projectID string, onComplete func(error)) (OAuthStartResult, error) {
	if c == nil {
		return OAuthStartResult{}, fmt.Errorf("oauth client unavailable")
	}
	tr, err := entry.Transport()
	if err != nil {
		return OAuthStartResult{}, err
	}
	if tr != TransportHTTP {
		return OAuthStartResult{}, fmt.Errorf("oauth is only supported for HTTP MCP providers")
	}
	resource := strings.TrimSpace(entry.URL)
	prm, scopes, err := c.discoverPRM(ctx, resource)
	if err != nil {
		return OAuthStartResult{}, err
	}
	if len(prm.AuthorizationServers) == 0 {
		return OAuthStartResult{}, fmt.Errorf("protected resource metadata has no authorization servers")
	}
	asm, err := mcpauth.GetAuthServerMetadata(ctx, prm.AuthorizationServers[0], c.http)
	if err != nil {
		return OAuthStartResult{}, fmt.Errorf("authorization server metadata: %w", err)
	}
	if asm == nil || strings.TrimSpace(asm.RegistrationEndpoint) == "" {
		return OAuthStartResult{}, ErrOAuthRegistrationRequired
	}

	if prm.Resource != "" {
		resource = prm.Resource
	}
	if len(scopes) == 0 {
		scopes = prm.ScopesSupported
	}

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return OAuthStartResult{}, err
	}
	state, err := randomURLString(24)
	if err != nil {
		return OAuthStartResult{}, err
	}

	// Bind the listener before dynamic client registration: the redirect URI is part of
	// what gets registered, so the port has to exist first.
	providerID := entry.ID
	listener, redirect, err := c.bindCallback(providerID, projectID, state, onComplete) //nolint:contextcheck // loopback Complete uses Background until the browser hits the redirect
	if err != nil {
		return OAuthStartResult{}, err
	}

	clientID, clientSecret, err := c.resolveClient(ctx, asm, redirect)
	if err != nil {
		listener.finish() //nolint:contextcheck // process-local listener teardown on start failure
		return OAuthStartResult{}, err
	}

	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  asm.AuthorizationEndpoint,
			TokenURL: asm.TokenEndpoint,
		},
		RedirectURL: redirect,
		Scopes:      scopes,
	}
	authURL := cfg.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("resource", resource),
	)

	c.mu.Lock()
	// Replacing a pending authorization retires the one it replaces, listener included.
	if prev, ok := c.pending[providerID]; ok {
		prev.listener.finish() //nolint:contextcheck // process-local listener teardown when replacing pending auth
	}
	c.pending[providerID] = pendingOAuth{
		ProviderID:   providerID,
		ProjectID:    projectID,
		State:        state,
		Verifier:     verifier,
		RedirectURI:  redirect,
		Resource:     resource,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AuthURL:      asm.AuthorizationEndpoint,
		TokenURL:     asm.TokenEndpoint,
		Scopes:       scopes,
		CreatedAt:    c.clock(),
		listener:     listener,
	}
	c.mu.Unlock()

	return OAuthStartResult{
		AuthorizeURL: authURL,
		State:        state,
		RedirectURI:  redirect,
	}, nil
}

// bindCallback starts the loopback listener for one authorization, or returns the
// pinned test redirect when one is configured.
func (c *OAuthClient) bindCallback(providerID, projectID, state string, onComplete func(error)) (*callbackListener, string, error) {
	if c.redirectOverride != "" {
		return nil, c.redirectOverride, nil
	}
	listener, err := startCallbackListener(func(res callbackResult) error {
		// State is checked here as well as in Complete: any local process can reach
		// this loopback handler, and a redirect without the issued state must not
		// redeem a code.
		if res.State == "" || res.State != state {
			return fmt.Errorf("oauth state mismatch")
		}
		err := c.Complete(context.Background(), providerID, projectID, OAuthCompleteRequest{
			Code:  res.Code,
			State: res.State,
		})
		if onComplete != nil {
			onComplete(err)
		}
		return err
	})
	if err != nil {
		return nil, "", err
	}
	redirect := listener.RedirectURI()
	return listener, redirect, nil
}

// Complete exchanges the authorization code for tokens.
func (c *OAuthClient) Complete(ctx context.Context, providerID, projectID string, req OAuthCompleteRequest) error {
	if c == nil {
		return fmt.Errorf("oauth client unavailable")
	}
	c.mu.Lock()
	pending, ok := c.pending[providerID]
	if ok && pending.expired(c.clock()) {
		delete(c.pending, providerID)
		c.mu.Unlock()
		pending.listener.finish() //nolint:contextcheck // process-local listener teardown on expired pending
		return fmt.Errorf("oauth authorization for %q expired; start sign-in again", providerID)
	}
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending oauth for %q", providerID)
	}
	if pending.ProjectID != projectID {
		return fmt.Errorf("oauth scope mismatch")
	}
	if req.State == "" || req.State != pending.State {
		return fmt.Errorf("oauth state mismatch")
	}
	if strings.TrimSpace(req.Code) == "" {
		return fmt.Errorf("oauth code is required")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", strings.TrimSpace(req.Code))
	form.Set("redirect_uri", pending.RedirectURI)
	form.Set("client_id", pending.ClientID)
	form.Set("code_verifier", pending.Verifier)
	form.Set("resource", pending.Resource)
	if pending.ClientSecret != "" {
		form.Set("client_secret", pending.ClientSecret)
	}

	tok, err := c.postToken(ctx, pending.TokenURL, form)
	if err != nil {
		return err
	}
	rec := OAuthTokenRecord{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    tok.TokenType,
		Expiry:       tok.Expiry,
		Resource:     pending.Resource,
		ClientID:     pending.ClientID,
		ClientSecret: pending.ClientSecret,
		TokenURL:     pending.TokenURL,
		AuthURL:      pending.AuthURL,
	}
	return c.commitAuthorization(providerID, pending, rec) //nolint:contextcheck // process-local authorization retirement
}

func (c *OAuthClient) commitAuthorization(providerID string, pending pendingOAuth, rec OAuthTokenRecord) error {
	c.mu.Lock()
	current, ok := c.pending[providerID]
	if !ok || current.State != pending.State || current.expired(c.clock()) {
		c.mu.Unlock()
		return fmt.Errorf("oauth authorization is no longer pending")
	}
	// Cancellation, replacement, and token publication share one commit boundary.
	err := c.store.Put(providerID, rec)
	if err == nil {
		delete(c.pending, providerID)
	}
	c.mu.Unlock()
	if err == nil {
		pending.listener.finish()
	}
	return err
}

// Cancel retires only the named attempt, preserving tokens and newer sign-ins.
func (c *OAuthClient) Cancel(providerID, projectID, state string) {
	if c == nil || state == "" {
		return
	}
	c.mu.Lock()
	pending, ok := c.pending[providerID]
	if ok && pending.State == state && pending.ProjectID == projectID {
		delete(c.pending, providerID)
	} else {
		ok = false
	}
	c.mu.Unlock()
	if ok {
		pending.listener.finish()
	}
}

// Revoke clears stored tokens and pending authorization for providerID.
func (c *OAuthClient) Revoke(providerID string) error {
	if c == nil || c.store == nil {
		return nil
	}
	c.mu.Lock()
	pending := c.pending[providerID]
	delete(c.pending, providerID)
	err := c.store.Delete(providerID)
	c.mu.Unlock()
	pending.listener.finish()
	return err
}

// Close stops every listener still waiting for a browser. Called when the registry
// shuts down so an abandoned sign-in does not hold a loopback port.
func (c *OAuthClient) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	pendings := make([]pendingOAuth, 0, len(c.pending))
	for id, p := range c.pending {
		pendings = append(pendings, p)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	for _, p := range pendings {
		p.listener.finish()
	}
}

// RefreshAccessToken silently refreshes when possible; a failure means the provider needs re-authorization.
func (c *OAuthClient) RefreshAccessToken(ctx context.Context, providerID string) (string, error) {
	if c == nil || c.store == nil {
		return "", fmt.Errorf("oauth client unavailable")
	}
	rec, ok := c.store.Get(providerID)
	if !ok {
		return "", fmt.Errorf("not signed in: %w", errReauthRequired)
	}
	if tok := c.store.AccessToken(providerID); tok != "" {
		return tok, nil
	}
	if rec.RefreshToken == "" || rec.TokenURL == "" {
		return "", fmt.Errorf("refresh unavailable: %w", errReauthRequired)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", rec.RefreshToken)
	form.Set("client_id", rec.ClientID)
	form.Set("resource", rec.Resource)
	if rec.ClientSecret != "" {
		form.Set("client_secret", rec.ClientSecret)
	}
	tok, err := c.postToken(ctx, rec.TokenURL, form)
	if err != nil {
		// Drop the grant only when the authorization server rejected the refresh
		// token itself; a network fault or a 5xx is transient.
		if isOAuthAuthFailure(err) {
			_ = c.store.Delete(providerID)
		}
		return "", err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = rec.RefreshToken
	}
	rec.AccessToken = tok.AccessToken
	rec.RefreshToken = tok.RefreshToken
	rec.TokenType = tok.TokenType
	rec.Expiry = tok.Expiry
	if err := c.store.Put(providerID, rec); err != nil {
		return "", err
	}
	return rec.AccessToken, nil
}

func (c *OAuthClient) resolveClient(ctx context.Context, asm *oauthex.AuthServerMeta, redirect string) (clientID, clientSecret string, err error) {
	if asm.RegistrationEndpoint == "" {
		return "", "", ErrOAuthRegistrationRequired
	}
	meta := &oauthex.ClientRegistrationMetadata{
		RedirectURIs:            []string{redirect},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		ClientName:              "Painted Wolf Code",
		ApplicationType:         "native",
	}
	reg, regErr := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, meta, c.http)
	if regErr != nil {
		return "", "", fmt.Errorf("%w: %w", ErrOAuthRegistrationRequired, regErr)
	}
	if reg == nil || strings.TrimSpace(reg.ClientID) == "" {
		return "", "", ErrOAuthRegistrationRequired
	}
	return reg.ClientID, reg.ClientSecret, nil
}

func (c *OAuthClient) discoverPRM(ctx context.Context, mcpServerURL string) (*oauthex.ProtectedResourceMetadata, []string, error) {
	// Probe the MCP endpoint for a 401 challenge with resource_metadata.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpServerURL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.http.Do(req)
	var challenges []oauthex.Challenge
	if err == nil && resp != nil {
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			challenges, _ = oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
		}
	}

	metaURL := resourceMetadataURLFromChallenges(challenges)
	for _, candidate := range protectedResourceMetadataURLs(metaURL, mcpServerURL) {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, candidate.url, candidate.resource, c.http)
		if err != nil || prm == nil {
			continue
		}
		if len(prm.AuthorizationServers) == 0 {
			return nil, nil, fmt.Errorf("protected resource metadata has no authorization servers")
		}
		return prm, scopesFromChallenges(challenges), nil
	}

	u, err := url.Parse(mcpServerURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse mcp url: %w", err)
	}
	root := *u
	root.Path = ""
	root.RawQuery = ""
	root.Fragment = ""
	return &oauthex.ProtectedResourceMetadata{
		AuthorizationServers: []string{root.String()},
		Resource:             mcpServerURL,
	}, scopesFromChallenges(challenges), nil
}

type prmCandidate struct {
	url      string
	resource string
}

func protectedResourceMetadataURLs(metadataURL, resourceURL string) []prmCandidate {
	var out []prmCandidate
	if metadataURL != "" {
		out = append(out, prmCandidate{url: metadataURL, resource: resourceURL})
	}
	ru, err := url.Parse(resourceURL)
	if err != nil {
		return out
	}
	mu := *ru
	mu.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(ru.Path, "/")
	out = append(out, prmCandidate{url: mu.String(), resource: resourceURL})
	mu.Path = "/.well-known/oauth-protected-resource"
	ruRoot := *ru
	ruRoot.Path = ""
	out = append(out, prmCandidate{url: mu.String(), resource: ruRoot.String()})
	return out
}

func resourceMetadataURLFromChallenges(cs []oauthex.Challenge) string {
	for _, c := range cs {
		if u := c.Params["resource_metadata"]; u != "" {
			return u
		}
	}
	return ""
}

func scopesFromChallenges(cs []oauthex.Challenge) []string {
	for _, c := range cs {
		if strings.EqualFold(c.Scheme, "bearer") && c.Params["scope"] != "" {
			return strings.Fields(c.Params["scope"])
		}
	}
	return nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (c *OAuthClient) postToken(ctx context.Context, tokenURL string, form url.Values) (*oauth2.Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &tokenEndpointError{Status: resp.StatusCode, Code: tokenErrorCode(body)}
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}
	tok := &oauth2.Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
	}
	if tr.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return tok, nil
}

// tokenErrorCode extracts the RFC 6749 error code from a token error body, or
// "" when the response is not a JSON error object.
func tokenErrorCode(body []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Error)
}

func generatePKCE() (verifier, challenge string, err error) {
	verifier, err = randomURLString(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomURLString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
