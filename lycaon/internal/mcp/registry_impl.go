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

// RegistryOptions configures RegistryImpl.
type RegistryOptions struct {
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

// RegistryImpl merges provider catalogs, connects providers, and registers tools.
type RegistryImpl struct {
	mu        sync.RWMutex
	sessionMu sync.Mutex
	// syncMu serializes catalog reload and discovery publication.
	syncMu sync.Mutex

	globalPath string
	statePath  string

	// pins detects remote HTTP tool-definition substitution after approval.
	pins *toolPins

	distro *DistroMCPConfig
	user   *UserMCPConfig
	// deviceCatalog is the distro+user merge consulted by the agent surface.
	deviceCatalog  []MergedMCPProviderEntry
	deviceRejected []RejectedRow

	sessions     map[sessionRef]*pooledSession
	breakers     map[string]*gobreaker.CircuitBreaker
	syncErrors   map[string]string // providerID -> catalog code ("" if healthy)
	syncOK       map[string]bool   // providerID -> last sync listed tools successfully
	authRequired map[string]bool   // providerID -> peer asked for MCP Authorization
	toolRefs     map[string]registeredTool
	// toolDefs retains sanitized definitions from the last sync per provider.
	toolDefs map[string][]sanitizedToolDefinition

	// overlayApplies gates the project layer on Applies("project_mcp", dir). Nil is
	// closed: distro + user layers only.
	overlayApplies func(ctx context.Context, projectDir string) bool

	connector    SessionConnector
	toolRegistry *tools.DefaultRegistry
	onSettings   func()
	breakerTh    uint32
	apiToken     string
	oauthStore   *OAuthTokenStore
	oauth        *OAuthClient
	recipes      *RecipeCatalog

	// deviceProbeRootsFn supplies roots for host-initiated inspection with no project.
	deviceProbeRootsFn func() []string

	// secretMatcher / secretAsk screen CallTool args before the RPC (nil ⇒ inert).
	secretMatcher *secretmatch.Matcher
	secretAsk     secretmatch.AskFunc

	// resync carries provider ids whose tool list changed; drained off the session reader.
	resync chan string

	// lifeCtx controls MCP session lifetimes (and stdio subprocesses). Canceled by Close.
	lifeCtx    context.Context
	lifeCancel context.CancelFunc
	closed     bool
}

// NewRegistryImpl constructs an MCP registry. Call Load before use.
func NewRegistryImpl(opts RegistryOptions) (*RegistryImpl, error) {
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
	r := &RegistryImpl{
		statePath:    statePath,
		pins:         newToolPins(statePath),
		globalPath:   globalPath,
		lifeCtx:      lifeCtx,
		lifeCancel:   lifeCancel,
		sessions:     map[sessionRef]*pooledSession{},
		breakers:     map[string]*gobreaker.CircuitBreaker{},
		syncErrors:   map[string]string{},
		syncOK:       map[string]bool{},
		authRequired: map[string]bool{},
		toolRefs:     map[string]registeredTool{},
		onSettings:   opts.OnSettingsChange,
		breakerTh:    th,
		oauthStore:   oauthStore,
		oauth:        opts.OAuthClient,
		recipes:      recipes,
		resync:       make(chan string, 16),
	}
	if r.oauth == nil {
		r.oauth = NewOAuthClient(oauthStore, opts.OAuthRedirectURI, nil)
	}
	conn := opts.Connector
	if conn == nil {
		conn = SDKConnector{AccessToken: r.accessTokenFor}
	}
	r.connector = conn
	go r.drainResync()
	return r, nil
}

// accessTokenFor supplies the OAuth bearer for an HTTP MCP provider from the device catalog.
func (r *RegistryImpl) accessTokenFor(providerID string) string {
	if _, ok := r.deviceEntry(providerID); !ok {
		return ""
	}
	return r.oauthStore.AccessToken(providerID)
}

// SetToolRegistry wires the host tool registry for dynamic mcp_* registration.
func (r *RegistryImpl) SetToolRegistry(reg *tools.DefaultRegistry) {
	r.mu.Lock()
	r.toolRegistry = reg
	r.mu.Unlock()
}

// SetAPIAccess configures the bearer token passed to first-party MCP subprocesses.
func (r *RegistryImpl) SetAPIAccess(token string) {
	r.mu.Lock()
	r.apiToken = strings.TrimSpace(token)
	r.mu.Unlock()
}

// SetDeviceProbeRoots installs roots for host-initiated inspection with no project selected.
func (r *RegistryImpl) SetDeviceProbeRoots(fn func() []string) {
	r.mu.Lock()
	r.deviceProbeRootsFn = fn
	r.mu.Unlock()
}

// SetProjectOverlayGate installs the Applies("project_mcp", dir) predicate consulted
// before any project MCP layer is read.
func (r *RegistryImpl) SetProjectOverlayGate(fn func(ctx context.Context, projectDir string) bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.overlayApplies = fn
	r.mu.Unlock()
}

// SetSecretScreen wires outbound secret screening for CallTool args.
// A nil or Inert() matcher leaves the seam inert.
func (r *RegistryImpl) SetSecretScreen(matcher *secretmatch.Matcher, ask secretmatch.AskFunc) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.secretMatcher = matcher
	r.secretAsk = ask
	r.mu.Unlock()
}

func (r *RegistryImpl) deviceProbeRoots() []string {
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
func (r *RegistryImpl) Load(ctx context.Context) error {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	return r.loadLocked(ctx)
}

func (r *RegistryImpl) loadLocked(ctx context.Context) error {
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

	r.closeSessionsNotRunnable(catalog)
	return r.syncToolsLocked(ctx)
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
func (r *RegistryImpl) deviceEntry(id string) (MCPProviderEntry, bool) {
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
func (r *RegistryImpl) deviceEnabled(id string) bool {
	entry, ok := r.deviceEntry(id)
	return ok && entry.Enabled
}

// ProviderConfigured reports whether id exists in the device catalog.
func (r *RegistryImpl) ProviderConfigured(id string) bool {
	if r == nil || id == "" {
		return false
	}
	_, ok := r.deviceEntry(id)
	return ok
}

// ProviderEnabled reports whether id is configured and enabled at device level.
func (r *RegistryImpl) ProviderEnabled(id string) bool {
	if r == nil || id == "" {
		return false
	}
	return r.deviceEnabled(id)
}

// EnabledProviderIDs lists the enabled device-catalog provider ids, sorted.
//
// Device level is the whole answer for callers describing what a session may reach: a
// project overlay can only take a provider away for its own tree, never add one.
func (r *RegistryImpl) EnabledProviderIDs() []string {
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
func (r *RegistryImpl) ResolveQualifiedTool(qualified string) (providerID, toolName string, ok bool) {
	if r == nil || qualified == "" {
		return "", "", false
	}
	r.mu.RLock()
	ref, found := r.toolRefs[strings.TrimSpace(qualified)]
	r.mu.RUnlock()
	if found {
		return ref.ProviderID, ref.ToolName, true
	}
	return "", "", false
}

// RegisteredMCPTools returns qualified tool names currently registered.
func (r *RegistryImpl) RegisteredMCPTools() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.toolRefs))
	for name := range r.toolRefs {
		out = append(out, name)
	}
	return out
}

// LastSyncError returns the catalog code for the most recent sync failure, or
// "" when the last sync saw it healthy or it is disabled.
func (r *RegistryImpl) LastSyncError(providerID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.syncErrors[providerID]
}

// projectView merges the device layer with one project's overlay for display and call-time narrowing.
// The project file is read fresh each time (not cached).
func (r *RegistryImpl) projectView(ctx context.Context, projectDir string) projectCatalogView {
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
func (r *RegistryImpl) ToolLoadingModes(ctx context.Context, projectDir string) map[string]bool {
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

func (r *RegistryImpl) projectOverlayApplies(ctx context.Context, projectDir string) bool {
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
func (r *RegistryImpl) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	refs := make([]sessionRef, 0, len(r.sessions))
	for ref := range r.sessions {
		refs = append(refs, ref)
	}
	r.mu.Unlock()
	for _, ref := range refs {
		r.closeSessionRef(ref)
	}
	// An abandoned sign-in still holds a loopback port; shutting the registry down
	// releases it rather than leaving it bound until the process exits.
	r.oauth.Close()
	r.lifeCancel()
	return nil
}
