package mcp

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/sony/gobreaker"
)

// ProviderCatalog owns scoped provider resolution and catalog observation.
type ProviderCatalog struct {
	mu             *sync.RWMutex
	Connections    *ConnectionPool
	Credentials    *ProviderCredentials
	Tools          *ToolDiscovery
	globalPath     string
	statePath      string
	distro         *DistroMCPConfig
	user           *UserMCPConfig
	deviceCatalog  []MergedMCPProviderEntry
	deviceRejected []RejectedRow
	overlayApplies func(ctx context.Context, projectDir string) bool
}

// ProviderAdministration owns provider overlay transactions.
type ProviderAdministration struct {
	mu          *sync.RWMutex
	Calls       *ToolCalls
	Catalog     *ProviderCatalog
	Connections *ConnectionPool
	Credentials *ProviderCredentials
	Tools       *ToolDiscovery
	onSettings  func()
}

// ProviderCredentials owns provider credential and OAuth state.
type ProviderCredentials struct {
	mu             *sync.RWMutex
	Administration *ProviderAdministration
	Catalog        *ProviderCatalog
	Connections    *ConnectionPool
	Tools          *ToolDiscovery
	oauthStore     *OAuthTokenStore
	oauth          *OAuthClient
	recipes        *RecipeCatalog
	authRequired   map[string]bool
}

// ConnectionPool owns provider sessions and subprocess lifetime.
type ConnectionPool struct {
	mu                 *sync.RWMutex
	Credentials        *ProviderCredentials
	Tools              *ToolDiscovery
	sessionMu          sync.Mutex
	sessions           map[sessionRef]*pooledSession
	connector          SessionConnector
	apiToken           string
	deviceProbeRootsFn func() []string
	lifeCtx            context.Context
	lifeCancel         context.CancelFunc
	closed             bool
}

// ToolDiscovery owns tool discovery and atomic publication.
type ToolDiscovery struct {
	mu           *sync.RWMutex
	Calls        *ToolCalls
	Catalog      *ProviderCatalog
	Connections  *ConnectionPool
	syncMu       sync.Mutex
	pins         *toolPins
	syncErrors   map[string]string
	syncOK       map[string]bool
	toolRefs     map[string]registeredTool
	toolDefs     map[string][]sanitizedToolDefinition
	toolRegistry *tools.DefaultRegistry
	resync       chan string
}

// ToolCalls owns screened provider invocation and circuit breakers.
type ToolCalls struct {
	mu            *sync.RWMutex
	Catalog       *ProviderCatalog
	Connections   *ConnectionPool
	Tools         *ToolDiscovery
	breakers      map[string]*gobreaker.CircuitBreaker
	breakerTh     uint32
	secretMatcher *secretmatch.Matcher
	secretAsk     secretmatch.AskFunc
}
