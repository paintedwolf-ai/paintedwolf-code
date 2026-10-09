package mcp

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/catalogruntime"
	"github.com/lycaon/lycaon/internal/destconfig"
	"github.com/lycaon/lycaon/pkg/api"
)

// overlayPathFor returns the file a mutation at this scope writes to.
func (r *ProviderAdministration) overlayPathFor(scopeProjectDir string) string {
	if dir := strings.TrimSpace(scopeProjectDir); dir != "" {
		return projectMCPPath(dir)
	}
	return r.Catalog.globalPath
}

// overlayLockHeldKey marks a context as already holding the catalog
// transaction lock for one overlay file.
type overlayLockHeldKey struct{}

// withOverlayLockHeld prevents nested reads from reacquiring the same transaction lock.
func withOverlayLockHeld(ctx context.Context, target string) context.Context {
	return context.WithValue(ctx, overlayLockHeldKey{}, filepath.Clean(target))
}

// overlayLockAlreadyHeld reports whether ctx's call chain already holds the
// transaction lock for target.
func overlayLockAlreadyHeld(ctx context.Context, target string) bool {
	held, _ := ctx.Value(overlayLockHeldKey{}).(string)
	return held != "" && held == filepath.Clean(target)
}

func (r *ProviderAdministration) updateOverlay(
	ctx context.Context,
	scopeProjectDir string,
	apply func(ctx context.Context, path string) error,
	validate func(ctx context.Context) error,
) error {
	path := r.overlayPathFor(scopeProjectDir)
	// Callbacks share the transaction lock for this overlay.
	lockedCtx := withOverlayLockHeld(ctx, path)
	if err := catalogruntime.UpdateFile(ctx, catalogruntime.FileUpdate{
		LockRoot: r.Catalog.statePath,
		Target:   path,
		Mode:     mcpFileMode,
		DirMode:  0o700,
		Apply: func() error {
			return apply(lockedCtx, path)
		},
		Publish: func() error {
			if strings.TrimSpace(scopeProjectDir) == "" {
				return r.Catalog.Load(lockedCtx)
			}
			return nil
		},
		Validate: func() error {
			if validate == nil {
				return nil
			}
			return validate(lockedCtx)
		},
	}); err != nil {
		return err
	}
	if r.onSettings != nil {
		r.onSettings()
	}
	return nil
}

// ListProviders returns the merged MCP catalog for a scope plus rejected trust rows.
func (r *ProviderCatalog) ListProviders(ctx context.Context, scope CallScope) []api.McpProvider {
	view := r.projectView(ctx, scope.ProjectDir)
	out := make([]api.McpProvider, 0, len(view.catalog)+len(view.rejected))
	for _, s := range view.catalog {
		out = append(out, r.wireProvider(s, view))
	}
	for _, rej := range view.rejected {
		id := rej.ID
		if id == "" {
			id = "(overlay)"
		}
		out = append(out, api.McpProvider{
			ID:               id,
			Enabled:          false,
			Class:            api.McpProviderClass(rej.Class),
			ToolLoading:      api.McpToolLoadingAuto,
			Status:           api.McpStatusRejected,
			LastError:        rej.Reason,
			ConnectionSource: string(rej.Layer),
		})
	}
	return out
}

// ConfiguredHosts returns the hostnames of every enabled HTTP MCP provider this
// project can reach. Hosts only — the URL carries a path and sometimes a token.
func (r *ProviderCatalog) ConfiguredHosts(ctx context.Context, projectDir string) []string {
	if r == nil {
		return nil
	}
	view := r.projectView(ctx, projectDir)
	var out []string
	for _, s := range view.catalog {
		if !s.Enabled {
			continue
		}
		if h := destconfig.Normalize(s.URL); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// GetProvider returns one wired provider row, or false if missing.
func (r *ProviderCatalog) GetProvider(ctx context.Context, scope CallScope, id string) (api.McpProvider, bool) {
	for _, row := range r.ListProviders(ctx, scope) {
		if row.ID == id {
			return row, true
		}
	}
	return api.McpProvider{}, false
}

// SetProviderEnabled persists overlay enabled and republishes.
func (r *ProviderAdministration) SetProviderEnabled(ctx context.Context, scope CallScope, providerID string, enabled bool, scopeProjectDir string) error {
	err := r.updateOverlay(ctx, scopeProjectDir, func(txCtx context.Context, path string) error {
		if !r.Catalog.projectView(txCtx, scope.ProjectDir).known(providerID) {
			return ErrUnknownMCPProvider(providerID)
		}
		return setProviderEnabled(path, providerID, enabled)
	}, nil)
	return persistErr(err)
}

// CreateProvider writes a new overlay row (user or project) and republishes.
func (r *ProviderAdministration) CreateProvider(ctx context.Context, scope CallScope, req api.CreateMcpProviderRequest, scopeProjectDir string) (api.McpProvider, error) {
	switch req.Source {
	case "recipe":
		recipeID := strings.TrimSpace(req.RecipeID)
		if recipeID == "" {
			return api.McpProvider{}, AdminErr(CodeIDRequired)
		}
		return r.AddRecipe(ctx, scope, recipeID, scopeProjectDir)
	case "custom":
	default:
		return api.McpProvider{}, AdminErr(RejectInvalidEntry)
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return api.McpProvider{}, AdminErr(CodeIDRequired)
	}
	if r.Catalog.projectView(ctx, scope.ProjectDir).known(id) {
		return api.McpProvider{}, AdminErrID(RejectDuplicateID, id)
	}

	ov, err := overlayFromCreate(id, req)
	if err != nil {
		return api.McpProvider{}, err
	}
	return r.persistNewOverlay(ctx, scope, id, ov, scopeProjectDir)
}

// AddRecipe copies a bundled recipe into the overlay catalog, disabled.
func (r *ProviderAdministration) AddRecipe(ctx context.Context, scope CallScope, recipeID, scopeProjectDir string) (api.McpProvider, error) {
	rec, ok := r.Credentials.recipes.Entry(recipeID)
	if !ok {
		return api.McpProvider{}, AdminErr(CodeRecipeNotFound)
	}
	if strings.TrimSpace(scopeProjectDir) != "" && !rec.ProjectOK() {
		return api.McpProvider{}, AdminErr(RejectProjectRemoteForbidden)
	}
	ov, ok := r.Credentials.recipes.OverlayFor(recipeID)
	if !ok {
		return api.McpProvider{}, AdminErr(CodeRecipeNotFound)
	}
	return r.persistNewOverlay(ctx, scope, ov.ID, ov, scopeProjectDir)
}

// ListRecipes returns bundled recipes with added/project_ok flags for this scope.
func (r *ProviderAdministration) ListRecipes(ctx context.Context, scope CallScope) []api.McpRecipe {
	view := r.Catalog.projectView(ctx, scope.ProjectDir)
	known := map[string]struct{}{}
	for _, s := range view.catalog {
		known[s.ID] = struct{}{}
	}
	for _, rej := range view.rejected {
		if rej.ID != "" {
			known[rej.ID] = struct{}{}
		}
	}
	entries := r.Credentials.recipes.Entries()
	out := make([]api.McpRecipe, 0, len(entries))
	for _, rec := range entries {
		_, added := known[rec.ID]
		envKeys := make([]api.McpRecipeEnvKey, 0, len(rec.EnvKeys))
		for _, ek := range rec.EnvKeys {
			envKeys = append(envKeys, api.McpRecipeEnvKey{Key: ek.Key, Label: ek.Label})
		}
		out = append(out, api.McpRecipe{
			ID:               rec.ID,
			Label:            rec.Label,
			Hint:             rec.Hint,
			DocsURL:          rec.DocsURL,
			URL:              rec.URL,
			Command:          rec.Command,
			Args:             append([]string(nil), rec.Args...),
			Auth:             api.McpRecipeAuth(rec.Auth),
			CredentialLabel:  rec.CredentialLabel,
			CredentialHint:   rec.CredentialHint,
			CredentialWire:   recipeCredentialWire(rec),
			CredentialHeader: rec.CredentialHeader,
			EnvKeys:          envKeys,
			Class:            api.McpProviderClass(rec.Class()),
			Added:            added,
			ProjectOK:        rec.ProjectOK(),
		})
	}
	return out
}

func (r *ProviderAdministration) persistNewOverlay(ctx context.Context, scope CallScope, id string, ov MCPProviderOverlay, scopeProjectDir string) (api.McpProvider, error) {
	var created api.McpProvider
	err := r.updateOverlay(ctx, scopeProjectDir, func(txCtx context.Context, path string) error {
		if r.Catalog.projectView(txCtx, scope.ProjectDir).known(id) {
			return AdminErrID(RejectDuplicateID, id)
		}
		return upsertProviderOverlay(path, ov)
	}, func(txCtx context.Context) error {
		row, ok := r.Catalog.GetProvider(txCtx, scope, id)
		if ok && row.Status != api.McpStatusRejected {
			created = row
			return nil
		}
		reason := RejectInvalidEntry
		if ok && row.LastError != "" {
			reason = row.LastError
		}
		return AdminErrID(reason, id)
	})
	if err != nil {
		return api.McpProvider{}, persistErr(err)
	}
	return created, nil
}

func overlayFromCreate(id string, req api.CreateMcpProviderRequest) (MCPProviderOverlay, error) {
	enabled := false
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ov := MCPProviderOverlay{ID: id, Enabled: &enabled}
	if req.ToolLoading != "" {
		if !validToolLoading(req.ToolLoading) {
			return MCPProviderOverlay{}, AdminErr(RejectInvalidEntry)
		}
		mode := req.ToolLoading
		ov.ToolLoading = &mode
	}
	url := strings.TrimSpace(req.URL)
	cmd := strings.TrimSpace(req.Command)
	switch {
	case url != "" && cmd != "":
		return MCPProviderOverlay{}, AdminErr(CodeTransportConflict)
	case url != "":
		canonical, err := overlayHTTPURL(url)
		if err != nil {
			return MCPProviderOverlay{}, err
		}
		ov.URL = &canonical
	case cmd != "":
		ov.Command = &cmd
		if req.Args != nil {
			args := append([]string(nil), req.Args...)
			ov.Args = &args
		}
	default:
		return MCPProviderOverlay{}, AdminErr(CodeTransportRequired)
	}
	if req.Env != nil {
		env := cloneStringMap(req.Env)
		ov.Env = &env
	}
	if req.Headers != nil {
		headers := cloneStringMap(req.Headers)
		ov.Headers = &headers
	}
	if t := strings.TrimSpace(req.Token); t != "" {
		ov.Token = &t
	}
	if req.CredentialWire != "" {
		if err := applyCredentialPlacement(&ov, string(req.CredentialWire), req.CredentialHeader); err != nil {
			return MCPProviderOverlay{}, err
		}
	}
	return ov, nil
}

// UpdateProvider patches overlay fields and republishes.
// Rejected ids are valid targets: the overlay row is still on disk.
func (r *ProviderAdministration) UpdateProvider(ctx context.Context, scope CallScope, providerID string, req api.UpdateMcpProviderRequest, scopeProjectDir string) (api.McpProvider, error) {
	if !updateNamesAField(req) {
		return api.McpProvider{}, AdminErr(CodeUpdateEmpty)
	}
	if req.ToolLoading != nil && !validToolLoading(*req.ToolLoading) {
		return api.McpProvider{}, AdminErr(RejectInvalidEntry)
	}
	if req.URL != nil {
		if u := strings.TrimSpace(*req.URL); u != "" {
			canonical, err := overlayHTTPURL(u)
			if err != nil {
				return api.McpProvider{}, err
			}
			req.URL = &canonical
		}
	}
	if err := validateUpdateCredential(req); err != nil {
		return api.McpProvider{}, err
	}

	var updated api.McpProvider
	err := r.updateOverlay(ctx, scopeProjectDir, func(txCtx context.Context, path string) error {
		if !r.Catalog.projectView(txCtx, scope.ProjectDir).known(providerID) {
			return ErrUnknownMCPProvider(providerID)
		}
		return patchProviderOverlay(path, providerID, func(ov *MCPProviderOverlay) {
			applyUpdateToOverlay(ov, req)
		})
	}, func(txCtx context.Context) error {
		row, ok := r.Catalog.GetProvider(txCtx, scope, providerID)
		if !ok {
			return ErrUnknownMCPProvider(providerID)
		}
		updated = row
		return nil
	})
	if err != nil {
		return api.McpProvider{}, persistErr(err)
	}
	return updated, nil
}

func updateNamesAField(req api.UpdateMcpProviderRequest) bool {
	return req.Enabled != nil || req.Command != nil ||
		req.Args != nil || req.URL != nil || req.Env != nil ||
		req.Headers != nil || req.Token != nil ||
		req.CredentialWire != nil || req.CredentialHeader != nil || req.ToolLoading != nil
}

// applyUpdateToOverlay writes a wire patch onto an overlay row.
// Switching transport also clears the other field: nil means inherit.
func applyUpdateToOverlay(ov *MCPProviderOverlay, req api.UpdateMcpProviderRequest) {
	empty := ""
	if req.Enabled != nil {
		ov.Enabled = req.Enabled
	}
	if req.ToolLoading != nil {
		ov.ToolLoading = req.ToolLoading
	}
	if req.URL != nil {
		u := strings.TrimSpace(*req.URL)
		ov.URL = &u
		ov.Command = &empty
		ov.Args = nil
	}
	if req.Command != nil {
		c := strings.TrimSpace(*req.Command)
		ov.Command = &c
		ov.URL = &empty
	}
	if req.Args != nil {
		args := append([]string(nil), (*req.Args)...)
		ov.Args = &args
	}
	if req.Env != nil {
		env := cloneStringMap(*req.Env)
		ov.Env = &env
	}
	if req.Headers != nil {
		headers := cloneStringMap(*req.Headers)
		ov.Headers = &headers
	}
	if req.Token != nil {
		t := strings.TrimSpace(*req.Token)
		ov.Token = &t
	}
	if req.CredentialWire != nil {
		header := ""
		if req.CredentialHeader != nil {
			header = *req.CredentialHeader
		}
		_ = applyCredentialPlacement(ov, string(*req.CredentialWire), header)
	} else if req.CredentialHeader != nil {
		h := strings.TrimSpace(*req.CredentialHeader)
		ov.CredentialHeader = &h
	}
}

func parseCredentialPlacement(wire, header string) (string, string, error) {
	kind, ok := NormalizeCredentialWire(wire)
	if !ok {
		return "", "", AdminErr(RejectInvalidEntry)
	}
	if kind == CredentialWireHeader {
		header = strings.TrimSpace(header)
		if !validCredentialHeader(header) {
			return "", "", AdminErr(RejectInvalidEntry)
		}
		return string(kind), header, nil
	}
	return string(kind), "", nil
}

func applyCredentialPlacement(ov *MCPProviderOverlay, wire, header string) error {
	w, h, err := parseCredentialPlacement(wire, header)
	if err != nil {
		return err
	}
	ov.CredentialWire = &w
	ov.CredentialHeader = &h
	return nil
}

func validateUpdateCredential(req api.UpdateMcpProviderRequest) error {
	if req.CredentialWire != nil {
		header := ""
		if req.CredentialHeader != nil {
			header = *req.CredentialHeader
		}
		_, _, err := parseCredentialPlacement(string(*req.CredentialWire), header)
		return err
	}
	if req.CredentialHeader != nil {
		h := strings.TrimSpace(*req.CredentialHeader)
		if h != "" && !validCredentialHeader(h) {
			return AdminErr(RejectInvalidEntry)
		}
	}
	return nil
}

func recipeCredentialWire(rec Recipe) api.McpCredentialWire {
	kind, _ := NormalizeCredentialWire(string(rec.CredentialWire))
	if kind == CredentialWireBearer {
		return ""
	}
	return api.McpCredentialWire(kind)
}

// DeleteOverlay removes a user or project overlay row for providerID.
func (r *ProviderAdministration) DeleteOverlay(ctx context.Context, providerID, scopeProjectDir string) error {
	err := r.updateOverlay(ctx, scopeProjectDir, func(txCtx context.Context, path string) error {
		if !r.Catalog.projectView(txCtx, scopeProjectDir).known(providerID) {
			return ErrUnknownMCPProvider(providerID)
		}
		return clearProviderOverride(path, providerID)
	}, nil)
	if err != nil {
		return persistErr(err)
	}
	r.Connections.closeProviderSessions(providerID)
	// Tokens are device-global, so only a device-scope delete revokes: a
	// project-override delete leaves the device provider (and its token) live.
	if r.Credentials.oauth != nil && strings.TrimSpace(scopeProjectDir) == "" {
		if revokeErr := r.Credentials.oauth.Revoke(providerID); revokeErr != nil { //nolint:contextcheck // Revoke tears down process-local OAuth state.
			slog.WarnContext(ctx, "mcp oauth revoke on delete failed", "provider_id", providerID, "error", revokeErr)
		}
	}
	return nil
}

func overlayHTTPURL(raw string) (string, error) {
	canonical, _, reject := ClassifyHTTPURL(raw)
	if reject != "" {
		return "", AdminErr(reject)
	}
	return canonical, nil
}

// ResyncProvider drops one provider's sessions and re-runs the sync.
func (r *ProviderAdministration) ResyncProvider(ctx context.Context, scope CallScope, providerID string) (api.McpProvider, error) {
	if !r.Catalog.projectView(ctx, scope.ProjectDir).known(providerID) {
		return api.McpProvider{}, ErrUnknownMCPProvider(providerID)
	}
	r.Connections.closeProviderSessions(providerID)
	if err := r.Tools.SyncTools(ctx); err != nil {
		return api.McpProvider{}, syncErr(err)
	}
	if row, ok := r.Catalog.GetProvider(ctx, scope, providerID); ok {
		return row, nil
	}
	return api.McpProvider{}, ErrUnknownMCPProvider(providerID)
}

// ListDiscoveredTools returns qualified tool infos for an enabled provider.
func (r *ProviderAdministration) ListDiscoveredTools(ctx context.Context, scope CallScope, providerID string) ([]api.McpToolInfo, error) {
	if !r.Catalog.projectView(ctx, scope.ProjectDir).known(providerID) {
		return nil, ErrUnknownMCPProvider(providerID)
	}
	metas, err := r.Calls.ListTools(ctx, scope, providerID)
	if err != nil {
		return nil, syncErr(err)
	}
	out := make([]api.McpToolInfo, 0, len(metas))
	for _, m := range metas {
		out = append(out, api.McpToolInfo{Name: m.Name, Description: m.Description})
	}
	return out, nil
}

// Check verifies each catalog provider for a scope and surfaces rejected trust rows.
func (r *ProviderAdministration) Check(ctx context.Context, scope CallScope) []api.McpCheckRow {
	view := r.Catalog.projectView(ctx, scope.ProjectDir)
	out := make([]api.McpCheckRow, 0, len(view.catalog)+len(view.rejected))
	for _, rej := range view.rejected {
		out = append(out, api.McpCheckRow{
			ProviderID: rej.ID,
			Status:     api.McpCheckRowStatusError,
			Code:       string(rej.Reason),
		})
	}
	for _, s := range view.catalog {
		row := api.McpCheckRow{ProviderID: s.ID}
		if !s.Enabled {
			row.Status = api.McpCheckRowStatusDisabled
			out = append(out, row)
			continue
		}
		sess, err := r.Connections.ensureSession(ctx, scope, s.MCPProviderEntry)
		if err != nil {
			row.Status = api.McpCheckRowStatusError
			row.Code = string(SyncFailureCode(err))
			out = append(out, row)
			continue
		}
		if _, err := sess.ListTools(ctx); err != nil {
			r.Connections.evictDeadSession(scope, s.ID, err)
			row.Status = api.McpCheckRowStatusError
			row.Code = string(SyncFailureCode(err))
			out = append(out, row)
			continue
		}
		row.Status = api.McpCheckRowStatusHealthy
		out = append(out, row)
	}
	return out
}
