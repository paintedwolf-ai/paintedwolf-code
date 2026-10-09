package mcp

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/pkg/api"
	"log/slog"
	"net/http"
	"strings"
)

// StartOAuth begins MCP Authorization for an HTTP provider.
func (r *ProviderCredentials) StartOAuth(ctx context.Context, scope CallScope, providerID string) (api.McpOAuthStartResponse, error) {
	entry, ok := r.Catalog.deviceEntry(providerID)
	if !ok {
		return api.McpOAuthStartResponse{}, ErrUnknownMCPProvider(providerID)
	}
	if r.oauth == nil {
		return api.McpOAuthStartResponse{}, AdminErr(CodeOAuthUnavailable)
	}
	if r.recipeForbidsOAuth(entry) {
		return api.McpOAuthStartResponse{}, AdminErr(CodeOAuthNotSupported)
	}
	res, err := r.oauth.Start(ctx, entry, scope.ProjectID, func(completeErr error) {
		r.onOAuthCallbackComplete(providerID, completeErr)
	})
	if err != nil {
		return api.McpOAuthStartResponse{}, oauthErr(err)
	}
	return api.McpOAuthStartResponse{
		AuthorizeURL: res.AuthorizeURL,
		State:        res.State,
		RedirectURI:  res.RedirectURI,
	}, nil
}

func (r *ProviderCredentials) stampRecipeCredential(entry MCPProviderEntry) MCPProviderEntry {
	if r == nil {
		return entry
	}
	rec, ok := r.recipes.Entry(entry.Recipe)
	if !ok {
		return entry
	}
	kind, _ := NormalizeCredentialWire(string(rec.CredentialWire))
	entry.CredentialWire = string(kind)
	entry.CredentialHeader = rec.CredentialHeader
	return entry
}

func (r *ProviderCredentials) recipeForbidsOAuth(entry MCPProviderEntry) bool {
	if strings.TrimSpace(entry.Recipe) == "" {
		return false
	}
	rec, ok := r.recipes.Entry(entry.Recipe)
	if !ok {
		return false
	}
	return rec.Auth != RecipeAuthOAuth
}

// onOAuthCallbackComplete applies the same post-exchange updates as CompleteOAuth.
func (r *ProviderCredentials) onOAuthCallbackComplete(providerID string, err error) {
	if err != nil {
		slog.Warn("mcp oauth callback failed", "provider_id", providerID, "error", err)
		return
	}
	r.mu.Lock()
	delete(r.authRequired, providerID)
	r.mu.Unlock()
	r.Connections.closeProviderSessions(providerID)
	if syncErr := r.Tools.SyncTools(r.Connections.lifeCtx); syncErr != nil {
		slog.Warn("mcp sync after oauth callback failed", "provider_id", providerID, "error", syncErr)
	}
	if r.Administration.onSettings != nil {
		r.Administration.onSettings()
	}
}

// CompleteOAuth finishes the authorization-code exchange and stores tokens.
func (r *ProviderCredentials) CompleteOAuth(ctx context.Context, scope CallScope, providerID string, req api.McpOAuthCompleteRequest) (api.McpProvider, error) {
	if _, ok := r.Catalog.deviceEntry(providerID); !ok {
		return api.McpProvider{}, ErrUnknownMCPProvider(providerID)
	}
	if r.oauth == nil {
		return api.McpProvider{}, AdminErr(CodeOAuthUnavailable)
	}
	if err := r.oauth.Complete(ctx, providerID, scope.ProjectID, OAuthCompleteRequest{Code: req.Code, State: req.State}); err != nil {
		return api.McpProvider{}, oauthErr(err)
	}
	r.mu.Lock()
	delete(r.authRequired, providerID)
	r.mu.Unlock()
	r.Connections.closeProviderSessions(providerID)
	// Sync faults land on the row as last_error, not this return.
	_ = r.Tools.SyncTools(ctx)
	if row, ok := r.Catalog.GetProvider(ctx, scope, providerID); ok {
		return row, nil
	}
	return api.McpProvider{}, ErrUnknownMCPProvider(providerID)
}

// CancelOAuth retires one pending sign-in without changing saved credentials.
func (r *ProviderCredentials) CancelOAuth(scope CallScope, providerID, state string) error {
	if _, ok := r.Catalog.deviceEntry(providerID); !ok {
		return ErrUnknownMCPProvider(providerID)
	}
	if r.oauth == nil {
		return AdminErr(CodeOAuthUnavailable)
	}
	r.oauth.Cancel(providerID, scope.ProjectID, state)
	return nil
}

// RevokeOAuth clears stored OAuth tokens (Sign out).
func (r *ProviderCredentials) RevokeOAuth(ctx context.Context, scope CallScope, providerID string) (api.McpProvider, error) {
	if _, ok := r.Catalog.deviceEntry(providerID); !ok {
		return api.McpProvider{}, ErrUnknownMCPProvider(providerID)
	}
	if r.oauth == nil {
		return api.McpProvider{}, AdminErr(CodeOAuthUnavailable)
	}
	if err := r.oauth.Revoke(providerID); err != nil { //nolint:contextcheck // Revoke tears down a process-local loopback listener
		return api.McpProvider{}, oauthErr(err)
	}
	r.markAuthRequired(providerID)
	r.Connections.closeProviderSessions(providerID)
	if row, ok := r.Catalog.GetProvider(ctx, scope, providerID); ok {
		return row, nil
	}
	return api.McpProvider{}, ErrUnknownMCPProvider(providerID)
}

// refreshHTTPAuth renews an OAuth access token before a connect that would otherwise
// present an expired one.
func (r *ProviderCredentials) refreshHTTPAuth(ctx context.Context, entry MCPProviderEntry) error {
	tr, err := entry.Transport()
	if err != nil {
		return err
	}
	if tr != TransportHTTP {
		return nil
	}
	if entry.HasStaticHTTPAuth() {
		return nil
	}
	if !r.oauthStore.SignedIn(entry.ID) {
		return nil
	}
	if _, err := r.oauth.RefreshAccessToken(ctx, entry.ID); err != nil {
		// Only an authorization failure invalidates the stored grant.
		if !isOAuthAuthFailure(err) {
			return fmt.Errorf("mcp provider %q: token refresh failed: %w", entry.ID, err)
		}
		r.markAuthRequired(entry.ID)
		return fmt.Errorf("mcp provider %q: needs_auth: %w", entry.ID, err)
	}
	return nil
}

// httpStatusObserver records authorization failures from wire status codes.
func (r *ProviderCredentials) httpStatusObserver(providerID string) func(int) {
	return func(code int) {
		switch code {
		case http.StatusUnauthorized, http.StatusForbidden:
			r.markAuthRequired(providerID)
		}
	}
}

func (r *ProviderCredentials) markAuthRequired(providerID string) {
	r.mu.Lock()
	r.authRequired[providerID] = true
	r.mu.Unlock()
}

// authRequiredNow reports whether providerID's most recent HTTP round trip
// came back 401/403 and the flag has not yet been cleared by a fresh connect.
func (r *ProviderCredentials) authRequiredNow(providerID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.authRequired[providerID]
}

// accessTokenFor supplies the OAuth bearer for an HTTP MCP provider from the device catalog.
func (r *ProviderCredentials) accessTokenFor(providerID string) string {
	if _, ok := r.Catalog.deviceEntry(providerID); !ok {
		return ""
	}
	return r.oauthStore.AccessToken(providerID)
}
