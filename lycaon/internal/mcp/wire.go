package mcp

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ProviderToolPrefix returns the host mcp_<provider>_ tool-name prefix.
func ProviderToolPrefix(providerID string) string {
	return "mcp_" + strings.ReplaceAll(strings.TrimSpace(providerID), "-", "_") + "_"
}

func (r *ProviderCatalog) wireProvider(s MergedMCPProviderEntry, view projectCatalogView) api.McpProvider {
	entry := api.McpProvider{
		ID:               s.ID,
		Enabled:          s.Enabled,
		Class:            api.McpProviderClass(s.Class()),
		ToolLoading:      s.NormalizedToolLoading(),
		ConnectionSource: string(s.ConnectionSource),
		Command:          s.Command,
		Args:             append([]string(nil), s.Args...),
		URL:              s.URL,
		EnvPresent:       len(s.Env) > 0,
		TokenPresent:     strings.TrimSpace(s.Token) != "",
		HeadersPresent:   len(s.Headers) > 0,
	}
	if tr, err := s.Transport(); err == nil {
		entry.Transport = string(tr)
	}

	r.mu.RLock()
	entry.Profiles = ProfilesForProvider(r.distro, s.ID)
	lastError := r.Tools.syncErrors[s.ID]
	synced := r.Tools.syncOK[s.ID]
	authNeeded := r.Credentials.authRequired[s.ID]
	toolCount := 0
	prefix := ProviderToolPrefix(s.ID)
	for name := range r.Tools.toolRefs {
		if strings.HasPrefix(name, prefix) {
			toolCount++
		}
	}
	r.mu.RUnlock()

	entry.SignedIn = r.Credentials.oauthStore.SignedIn(s.ID)
	if entry.Enabled {
		entry.LastError = lastError
	}
	if entry.Enabled || toolCount > 0 {
		entry.ToolCount = &toolCount
	}
	entry.Status = providerStatus(s, statusFacts{
		lastError:  lastError,
		synced:     synced,
		authNeeded: authNeeded,
		signedIn:   entry.SignedIn,
		toolCount:  toolCount,
	})
	if view.applies {
		if _, ok := view.overrides[s.ID]; ok {
			entry.Source = "override"
		} else {
			entry.Source = "default"
		}
	}
	if rec, ok := r.Credentials.recipes.Entry(s.Recipe); ok {
		entry.Recipe = rec.ID
		entry.Auth = api.McpRecipeAuth(rec.Auth)
		entry.CredentialLabel = rec.CredentialLabel
		entry.CredentialHint = rec.CredentialHint
		entry.DocsURL = rec.DocsURL
		entry.CredentialWire = recipeCredentialWire(rec)
		entry.CredentialHeader = rec.CredentialHeader
	} else if kind, ok := NormalizeCredentialWire(s.CredentialWire); ok && kind != CredentialWireBearer {
		entry.CredentialWire = api.McpCredentialWire(kind)
		entry.CredentialHeader = s.CredentialHeader
	}
	return entry
}

// authNeeded is the HTTP status the transport observed.
type statusFacts struct {
	lastError  string
	synced     bool
	authNeeded bool
	signedIn   bool
	toolCount  int
}

func providerStatus(s MergedMCPProviderEntry, f statusFacts) api.McpProviderStatus {
	if !s.Enabled {
		return api.McpStatusDisabled
	}
	if f.authNeeded && !f.signedIn && !s.HasStaticHTTPAuth() {
		return api.McpStatusNeedsAuth
	}
	if f.lastError != "" {
		if f.authNeeded {
			return api.McpStatusNeedsAuth
		}
		return api.McpStatusError
	}
	if f.synced || f.toolCount > 0 {
		return api.McpStatusReady
	}
	return api.McpStatusConnecting
}
