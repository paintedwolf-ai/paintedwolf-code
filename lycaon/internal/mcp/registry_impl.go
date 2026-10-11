// Package mcp integrates configured providers with host tools.
package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/sony/gobreaker"
)

const defaultBreakerThreshold = 5

// registeredTool maps a host-qualified mcp_* name back to catalog identity.
type registeredTool struct {
	ProviderID string
	ToolName   string
}

// RuntimeOptions configures Runtime.
type RuntimeOptions struct {
	// StatePath overrides the user config directory that holds device-level MCP
	// state (tool pins). Empty resolves configdir.UserConfigDir.
	StatePath          string
	GlobalOverridePath string
	Connector          SessionConnector
	BreakerThreshold   uint32
	OnSettingsChange   func()
	OAuthStore         *OAuthTokenStore
	OAuthClient        *OAuthClient
	OAuthRedirectURI   string
}

// Runtime composes provider catalogs, connections, credentials and tools.
type Runtime struct {
	Catalog        *ProviderCatalog
	Administration *ProviderAdministration
	Credentials    *ProviderCredentials
	Connections    *ConnectionPool
	Tools          *ToolDiscovery
	Calls          *ToolCalls
}

// NewRuntime constructs an MCP registry. Call Load before use.
func NewRuntime(opts RuntimeOptions) (*Runtime, error) {
	th := opts.BreakerThreshold
	if th == 0 {
		th = defaultBreakerThreshold
	}
	oauthStore := opts.OAuthStore
	if oauthStore == nil {
		var err error
		oauthStore, err = NewOAuthTokenStore()
		if err != nil {
			return nil, fmt.Errorf("mcp oauth credentials: %w", err)
		}
	}
	statePath := strings.TrimSpace(opts.StatePath)
	if statePath == "" {
		var err error
		statePath, err = configdir.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("mcp config directory: %w", err)
		}
	}
	globalPath := filepath.Join(statePath, settingsoverlay.BasenameMCP)
	if opts.GlobalOverridePath != "" {
		globalPath = opts.GlobalOverridePath
	}
	recipes, err := LoadRecipeCatalog()
	if err != nil {
		return nil, fmt.Errorf("mcp recipe catalog: %w", err)
	}
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	coordination := &sync.RWMutex{}
	r := &Runtime{
		Catalog:        &ProviderCatalog{mu: coordination},
		Administration: &ProviderAdministration{mu: coordination},
		Credentials:    &ProviderCredentials{mu: coordination},
		Connections:    &ConnectionPool{mu: coordination},
		Tools:          &ToolDiscovery{mu: coordination},
		Calls:          &ToolCalls{mu: coordination},
	}
	r.Catalog.statePath = statePath
	r.Tools.pins = newToolPins(statePath)
	r.Catalog.globalPath = globalPath
	r.Connections.lifeCtx = lifeCtx
	r.Connections.lifeCancel = lifeCancel
	r.Connections.sessions = map[sessionRef]*pooledSession{}
	r.Calls.breakers = map[string]*gobreaker.CircuitBreaker{}
	r.Tools.syncErrors = map[string]string{}
	r.Tools.syncOK = map[string]bool{}
	r.Credentials.authRequired = map[string]bool{}
	r.Tools.toolRefs = map[string]registeredTool{}
	r.Administration.onSettings = opts.OnSettingsChange
	r.Calls.breakerTh = th
	r.Credentials.oauthStore = oauthStore
	r.Credentials.oauth = opts.OAuthClient
	r.Credentials.recipes = recipes
	r.Tools.resync = make(chan string, 16)

	r.Catalog.Connections = r.Connections
	r.Catalog.Credentials = r.Credentials
	r.Catalog.Tools = r.Tools
	r.Administration.Calls = r.Calls
	r.Administration.Catalog = r.Catalog
	r.Administration.Connections = r.Connections
	r.Administration.Credentials = r.Credentials
	r.Administration.Tools = r.Tools
	r.Credentials.Administration = r.Administration
	r.Credentials.Catalog = r.Catalog
	r.Credentials.Connections = r.Connections
	r.Credentials.Tools = r.Tools
	r.Connections.Credentials = r.Credentials
	r.Connections.Tools = r.Tools
	r.Tools.Calls = r.Calls
	r.Tools.Catalog = r.Catalog
	r.Tools.Connections = r.Connections
	r.Calls.Catalog = r.Catalog
	r.Calls.Connections = r.Connections
	r.Calls.Tools = r.Tools
	if r.Credentials.oauth == nil {
		r.Credentials.oauth = NewOAuthClient(oauthStore, opts.OAuthRedirectURI, nil)
	}
	conn := opts.Connector
	if conn == nil {
		conn = SDKConnector{AccessToken: r.Credentials.accessTokenFor}
	}
	r.Connections.connector = conn
	go r.Tools.drainResync()
	return r, nil
}

// SetToolRegistry wires the host tool registry for dynamic mcp_* registration.
func (r *ToolDiscovery) SetToolRegistry(reg *tools.DefaultRegistry) {
	r.mu.Lock()
	r.toolRegistry = reg
	r.mu.Unlock()
}

// SetAPIAccess configures the bearer token passed to first-party MCP subprocesses.
func (r *ConnectionPool) SetAPIAccess(token string) {
	r.mu.Lock()
	r.apiToken = strings.TrimSpace(token)
	r.mu.Unlock()
}

// SetDeviceProbeRoots installs roots for host-initiated inspection with no project selected.
func (r *ConnectionPool) SetDeviceProbeRoots(fn func() []string) {
	r.mu.Lock()
	r.deviceProbeRootsFn = fn
	r.mu.Unlock()
}

// SetProjectOverlayGate installs the Applies("project_mcp", dir) predicate consulted
// before any project MCP layer is read.
func (r *ProviderCatalog) SetProjectOverlayGate(fn func(ctx context.Context, projectDir string) bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.overlayApplies = fn
	r.mu.Unlock()
}

// SetSecretScreen wires outbound secret screening for CallTool args.
// A nil or Inert() matcher leaves the seam inert.
func (r *ToolCalls) SetSecretScreen(matcher *secretmatch.Matcher, ask secretmatch.AskFunc) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.secretMatcher = matcher
	r.secretAsk = ask
	r.mu.Unlock()
}

func (r *ConnectionPool) deviceProbeRoots() []string {
	r.mu.RLock()
	fn := r.deviceProbeRootsFn
	r.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return cleanRootPaths(fn())
}

// Load reads distro + user overlays into the device catalog and re-syncs tools.
// An unreadable user overlay is dropped as a rejected row; an unreadable distro catalog is fatal.
func (r *ProviderCatalog) Load(ctx context.Context) error {
	r.Tools.syncMu.Lock()
	defer r.Tools.syncMu.Unlock()
	return r.loadLocked(ctx)
}

func (r *ProviderCatalog) loadLocked(ctx context.Context) error {
	distro, err := LoadDistroMCPConfig()
	if err != nil {
		return fmt.Errorf("mcp distro catalog: %w", err)
	}

	var layerRejected []RejectedRow
	user, err := LoadUserMCPConfig(r.globalPath)
	if err != nil {
		slog.WarnContext(ctx, "mcp user overlay unusable; dropping layer",
			"path", r.globalPath, "error", err)
		layerRejected = append(layerRejected, RejectedRow{
			Layer:  CatalogLayerUser,
			Reason: unreadableLayerReason(err),
			Class:  ProviderClassWeb,
		})
		user = &UserMCPConfig{}
	}

	catalog, rejected, err := MergeMCPCatalog(distro, user, nil)
	if err != nil {
		return fmt.Errorf("mcp catalog merge: %w", err)
	}

	r.mu.Lock()
	r.distro = distro
	r.user = user
	r.deviceCatalog = catalog
	r.deviceRejected = append(append([]RejectedRow(nil), layerRejected...), rejected...)
	r.mu.Unlock()

	r.Connections.closeSessionsNotRunnable(catalog)
	return r.Tools.syncToolsLocked(ctx)
}

// unreadableLayerReason maps a layer read failure to a closed-set rejection reason.
// Field-derived: a duplicate id is reported by the loader as its own error type, and
// everything else is an unreadable layer.
func unreadableLayerReason(err error) string {
	if IsDuplicateIDError(err) {
		return RejectDuplicateID
	}
	if IsOverlayUnknownFieldError(err) {
		return RejectOverlayUnknownField
	}
	return RejectUnreadableLayer
}

// deviceEntry returns the device-catalog row for id.
func (r *ProviderCatalog) deviceEntry(id string) (MCPProviderEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.deviceCatalog {
		if s.ID == id {
			return s.MCPProviderEntry, true
		}
	}
	return MCPProviderEntry{}, false
}

// deviceEnabled reports whether id is present and enabled in the device catalog.
func (r *ProviderCatalog) deviceEnabled(id string) bool {
	entry, ok := r.deviceEntry(id)
	return ok && entry.Enabled
}

// ProviderConfigured reports whether id exists in the device catalog.
func (r *ProviderCatalog) ProviderConfigured(id string) bool {
	if r == nil || id == "" {
		return false
	}
	_, ok := r.deviceEntry(id)
	return ok
}

// ProviderEnabled reports whether id is configured and enabled at device level.
func (r *ProviderCatalog) ProviderEnabled(id string) bool {
	if r == nil || id == "" {
		return false
	}
	return r.deviceEnabled(id)
}

// EnabledProviderIDs lists the enabled device-catalog provider ids, sorted.
//
// Device level is the whole answer for callers describing what a session may reach: a
// project overlay can only take a provider away for its own tree, never add one.
func (r *ProviderCatalog) EnabledProviderIDs() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	out := make([]string, 0, len(r.deviceCatalog))
	for _, s := range r.deviceCatalog {
		if s.Enabled {
			out = append(out, s.ID)
		}
	}
	r.mu.RUnlock()
	sort.Strings(out)
	return out
}

// ResolveQualifiedTool maps a host mcp_* name to catalog provider id + tool name.
func (r *ProviderCatalog) ResolveQualifiedTool(qualified string) (providerID, toolName string, ok bool) {
	if r == nil || qualified == "" {
		return "", "", false
	}
	r.mu.RLock()
	ref, found := r.Tools.toolRefs[strings.TrimSpace(qualified)]
	r.mu.RUnlock()
	if found {
		return ref.ProviderID, ref.ToolName, true
	}
	return "", "", false
}

// RegisteredMCPTools returns qualified tool names currently registered.
func (r *ProviderCatalog) RegisteredMCPTools() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.Tools.toolRefs))
	for name := range r.Tools.toolRefs {
		out = append(out, name)
	}
	return out
}

// LastSyncError returns the catalog code for the most recent sync failure, or
// "" when the last sync saw it healthy or it is disabled.
func (r *ProviderCatalog) LastSyncError(providerID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.Tools.syncErrors[providerID]
}

// projectView merges the device layer with one project's overlay for display and call-time narrowing.
// The project file is read fresh each time (not cached).
func (r *ProviderCatalog) projectView(ctx context.Context, projectDir string) projectCatalogView {
	r.mu.RLock()
	distro := r.distro
	user := r.user
	device := append([]MergedMCPProviderEntry(nil), r.deviceCatalog...)
	deviceRejected := append([]RejectedRow(nil), r.deviceRejected...)
	r.mu.RUnlock()

	view := projectCatalogView{
		catalog:  device,
		rejected: deviceRejected,
	}
	dir := strings.TrimSpace(projectDir)
	if dir == "" || !r.projectOverlayApplies(ctx, dir) {
		// Capability not approved or project_mcp surface off: the layer does not apply.
		// There is nothing wrong with the file, so it is dropped rather than reported.
		return view
	}
	view.applies = true

	// Join the same catalog file-transaction lock a project overlay write holds,
	// so this read can never observe a row mid-apply or about to be rolled back.
	// A call already inside that transaction (Apply/Publish/Validate for this
	// same file) reads straight through instead: the lock is not reentrant.
	projectPath := projectMCPPath(dir)
	var project *UserMCPConfig
	var err error
	if overlayLockAlreadyHeld(ctx, projectPath) {
		project, err = LoadUserMCPConfig(projectPath)
	} else {
		project, err = LoadUserMCPConfigLocked(ctx, r.statePath, projectPath)
	}
	if err != nil {
		slog.WarnContext(ctx, "mcp project overlay unusable; dropping layer",
			"path", projectMCPPath(dir), "error", err)
		view.rejected = append(view.rejected, RejectedRow{
			Layer:  CatalogLayerProject,
			Reason: unreadableLayerReason(err),
			Class:  ProviderClassWeb,
		})
		return view
	}
	catalog, rejected, err := MergeMCPCatalog(distro, user, project)
	if err != nil {
		view.rejected = append(view.rejected, RejectedRow{
			Layer:  CatalogLayerProject,
			Reason: RejectInvalidEntry,
			Class:  ProviderClassWeb,
		})
		return view
	}
	view.catalog = catalog
	view.rejected = append(append([]RejectedRow(nil), deviceRejected...), rejected...)
	view.overrides = OverrideIDs(project)
	return view
}

// projectCatalogView is the device catalog as one project sees it.
type projectCatalogView struct {
	catalog  []MergedMCPProviderEntry
	rejected []RejectedRow
	// overrides names the ids the project overlay speaks about.
	overrides map[string]struct{}
	// applies reports whether the project layer was merged at all.
	applies bool
}

func (v projectCatalogView) entry(id string) (MergedMCPProviderEntry, bool) {
	for _, s := range v.catalog {
		if s.ID == id {
			return s, true
		}
	}
	return MergedMCPProviderEntry{}, false
}

// ToolLoadingModes resolves provider loading modes for one project view.
func (r *ProviderCatalog) ToolLoadingModes(ctx context.Context, projectDir string) map[string]bool {
	if r == nil {
		return nil
	}
	view := r.projectView(ctx, projectDir)
	out := make(map[string]bool, len(view.catalog))
	for _, entry := range view.catalog {
		out[entry.ID] = entry.AlwaysLoadsTools()
	}
	return out
}

// known includes accepted and rejected IDs because both reserve the ID.
func (v projectCatalogView) known(id string) bool {
	if _, ok := v.entry(id); ok {
		return true
	}
	for _, rej := range v.rejected {
		if rej.ID == id {
			return true
		}
	}
	return false
}

func (r *ProviderCatalog) projectOverlayApplies(ctx context.Context, projectDir string) bool {
	r.mu.RLock()
	fn := r.overlayApplies
	r.mu.RUnlock()
	if fn == nil || strings.TrimSpace(projectDir) == "" {
		return false
	}
	return fn(ctx, projectDir)
}

// Close shuts down all MCP sessions and the subprocesses behind them. It is terminal:
// the lifetime context every future spawn would inherit is canceled.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	err := r.Connections.Close()
	r.Credentials.oauth.Close(ctx)
	return err
}
