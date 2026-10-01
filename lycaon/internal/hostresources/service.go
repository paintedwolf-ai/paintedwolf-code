package hostresources

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/catalogruntime"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

const (
	DefaultTTL          = 30 * time.Second
	SnapshotTimeout     = 5 * time.Second
	DiscoveryConcurrent = 8
)

type Service struct {
	mu                sync.Mutex
	builtinDefs       []Definition
	builtinGuidance   map[string]PromptMode
	defs              []Definition
	env               discoveryEnvironment
	ttl               time.Duration
	now               func() time.Time
	cache             *catalogruntime.SnapshotCache[Snapshot]
	diagnostics       []Diagnostic
	userCatalogFile   string
	policyBinder      PolicyBinder
	connectionSupport connectionSupportSource
}

// PolicyDecision contains effective access and its exact editable setting.
type PolicyDecision struct {
	Access  Access
	Setting AccessSetting
}

// ProjectContext identifies the project policy layer.
type ProjectContext struct {
	ID  string
	Dir string
}

// PolicyEvaluator decides access for one resource against already-bound layers.
type PolicyEvaluator func(id, family string) PolicyDecision

// PolicyBinder binds project policy once per snapshot. SnapshotFor calls it
// once, then evaluates each resource in memory. The binder must not re-enter
// catalog compile per resource.
type PolicyBinder func(context.Context, ProjectContext) PolicyEvaluator

// connectionSupportSource reports supported routes for a platform.
type connectionSupportSource func(platform string, connection Connection) bool

func UserCatalogPath(configDir string) string {
	return filepath.Join(configDir, "host-resources.yaml")
}

func NewService(configDir string) (*Service, error) {
	builtinData, err := config.Read(config.HostResources)
	if err != nil {
		return nil, fmt.Errorf("read host resources catalog: %w", err)
	}
	builtin, err := ParseCatalog(builtinData, "built-in")
	if err != nil {
		return nil, err
	}
	service := &Service{
		builtinDefs:       append([]Definition(nil), builtin.Resources...),
		builtinGuidance:   cloneGuidance(builtin.Guidance),
		env:               defaultDiscoveryEnvironment(configDir),
		ttl:               DefaultTTL,
		now:               time.Now,
		connectionSupport: defaultConnectionSupport,
		userCatalogFile:   UserCatalogPath(configDir),
	}
	service.cache = catalogruntime.NewSnapshotCacheWithClock(service.ttl, cloneSnapshot, service.now)
	service.reloadDefinitions()
	return service, nil
}

func defaultConnectionSupport(platform string, connection Connection) bool {
	switch connection.Mode {
	case ConnectionNone, ConnectionProxy, ConnectionSOCKS,
		ConnectionBaselineLoopback, ConnectionDynamic:
		return true
	case ConnectionLocalService:
		return platform == "macos" && connection.Transport == LocalServiceUnixSocket
	case ConnectionDirectIP:
		return platform == "macos"
	default:
		return false
	}
}

// SetPolicyBinder sets the effective access policy binder.
func (s *Service) SetPolicyBinder(binder PolicyBinder) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policyBinder = binder
}

func (s *Service) reloadDefinitions() {
	definitions := append([]Definition(nil), s.builtinDefs...)
	guidance := cloneGuidance(s.builtinGuidance)
	var diagnostics []Diagnostic
	if userData, readErr := readCatalogFile(s.userCatalogFile); readErr == nil {
		user, parseErr := ParseCatalog(userData, "user")
		if parseErr != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Code: "HOST_RESOURCES_USER_CATALOG_INVALID", Message: parseErr.Error(),
			})
		} else {
			merged, mergeErr := mergeDefinitions(definitions, user.Resources)
			if mergeErr != nil {
				diagnostics = append(diagnostics, Diagnostic{
					Code: "HOST_RESOURCES_USER_CATALOG_CONFLICT", Message: mergeErr.Error(),
				})
			} else {
				definitions = merged
				for id, mode := range user.Guidance {
					guidance[id] = mode
				}
			}
		}
	} else if !os.IsNotExist(readErr) {
		diagnostics = append(diagnostics, Diagnostic{
			Code:    "HOST_RESOURCES_USER_CATALOG_READ_FAILED",
			Message: "User host resources catalog could not be read.",
		})
	}
	known := make(map[string]*Definition, len(definitions))
	for i := range definitions {
		known[definitions[i].ID] = &definitions[i]
	}
	for id, mode := range guidance {
		def, ok := known[id]
		if !ok {
			diagnostics = append(diagnostics, Diagnostic{
				Code: "HOST_RESOURCES_GUIDANCE_UNKNOWN", Message: fmt.Sprintf("Host-resource guidance references unknown id %q.", id),
			})
			continue
		}
		def.prompt = mode
	}
	s.defs = definitions
	s.diagnostics = diagnostics
}

func cloneGuidance(in map[string]PromptMode) map[string]PromptMode {
	out := make(map[string]PromptMode, len(in))
	for id, mode := range in {
		out[id] = mode
	}
	return out
}

func readCatalogFile(path string) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- fixed device config path selected by the host.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, CatalogBytesMax+1))
	if err != nil {
		return nil, err
	}
	if len(data) > CatalogBytesMax {
		return nil, fmt.Errorf("catalog exceeds %d bytes", CatalogBytesMax)
	}
	return data, nil
}

func mergeDefinitions(builtin, user []Definition) ([]Definition, error) {
	layers := []catalogruntime.Layer[Definition]{
		{Name: "bundled host resources", Items: definitionItems(builtin)},
		{Name: "user host resources", Items: definitionItems(user)},
	}
	catalog, err := catalogruntime.Assemble(layers, func(existing catalogruntime.Item[Definition], exists bool, incoming catalogruntime.Item[Definition]) (catalogruntime.Item[Definition], error) {
		if !exists {
			return incoming, nil
		}
		return existing, fmt.Errorf("user host resources catalog cannot replace %q", incoming.ID)
	})
	if err != nil {
		return nil, err
	}
	items := catalog.Items()
	merged := make([]Definition, 0, len(items))
	for _, item := range items {
		merged = append(merged, item.Spec)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID < merged[j].ID })
	return merged, nil
}

func definitionItems(definitions []Definition) []catalogruntime.Item[Definition] {
	items := make([]catalogruntime.Item[Definition], 0, len(definitions))
	for _, definition := range definitions {
		items = append(items, catalogruntime.Item[Definition]{
			ID: definition.ID, Spec: definition,
		})
	}
	return items
}

func (s *Service) Snapshot(ctx context.Context, refresh bool) Snapshot {
	return s.SnapshotFor(ctx, refresh, ProjectContext{}, nil)
}

// SnapshotFor applies project policy and trusted guidance.
func (s *Service) SnapshotFor(ctx context.Context, refresh bool, project ProjectContext, overlayRoots []string) Snapshot {
	base := s.discoverySnapshot(ctx, refresh)
	if s == nil {
		return base
	}
	s.mu.Lock()
	policyBinder := s.policyBinder
	s.mu.Unlock()
	var decide PolicyEvaluator
	if policyBinder != nil {
		decide = policyBinder(ctx, project)
	}
	projectGuidance, projectDiags := loadProjectGuidance(overlayRoots)
	known := make(map[string]struct{}, len(base.Resources))
	for _, state := range base.Resources {
		known[state.ID] = struct{}{}
	}
	var unknownGuidance []string
	for id := range projectGuidance {
		if _, ok := known[id]; ok {
			continue
		}
		delete(projectGuidance, id)
		unknownGuidance = append(unknownGuidance, id)
	}
	sort.Strings(unknownGuidance)
	for _, id := range unknownGuidance {
		projectDiags = append(projectDiags, Diagnostic{
			Code: "HOST_RESOURCES_PROJECT_GUIDANCE_UNKNOWN", Message: fmt.Sprintf("Project host-resource guidance references unknown id %q.", id),
		})
	}
	for i := range base.Resources {
		state := &base.Resources[i]
		state.Access = AccessAllow
		state.AccessSetting = AccessSettingInherit
		if decide != nil {
			decision := decide(state.ID, state.Family)
			if decision.Access == AccessAsk || decision.Access == AccessDeny {
				state.Access = decision.Access
			}
			if decision.Setting == AccessSettingAsk || decision.Setting == AccessSettingDeny {
				state.AccessSetting = decision.Setting
			}
		}
		if mode, ok := projectGuidance[state.ID]; ok {
			state.Prompt = mode
		}
	}
	base.Diagnostics = append(base.Diagnostics, projectDiags...)
	base.Fingerprint = snapshotFingerprint(base)
	return base
}

func (s *Service) discoverySnapshot(ctx context.Context, refresh bool) Snapshot {
	if s == nil {
		now := time.Now().UTC()
		return Snapshot{
			Version: CatalogVersion, Resources: []State{}, Diagnostics: []Diagnostic{},
			UserCatalogPath: configdir.Label() + "/host-resources.yaml", CheckedAt: now,
			Fingerprint: snapshotFingerprint(Snapshot{}),
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	cache := s.ensureCacheLocked()
	if cached, present, fresh, _ := cache.Read(); !refresh && present && fresh {
		return cached
	}
	if s.userCatalogFile != "" {
		s.reloadDefinitions()
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, SnapshotTimeout)
	defer cancel()
	states := make([]State, len(s.defs))
	semaphore := make(chan struct{}, DiscoveryConcurrent)
	var wait sync.WaitGroup
	for i, def := range s.defs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			realization := realizationForPlatform(def.Realizations, s.env.platform)
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-discoveryCtx.Done():
				states[i] = stateFromVerdict(def, realization, verdict{
					status: StatusUnknown, reason: "discovery_deadline",
				}, s.env.platform, s.connectionSupport, now)
				return
			}
			got, selected := discover(discoveryCtx, def, s.env)
			states[i] = stateFromVerdict(def, selected, got, s.env.platform, s.connectionSupport, now)
		}()
	}
	wait.Wait()
	snapshot := Snapshot{
		Version: CatalogVersion, Resources: states,
		Diagnostics:     append([]Diagnostic(nil), s.diagnostics...),
		UserCatalogPath: s.userCatalogFile, CheckedAt: now,
	}
	_, _, _, generation := cache.Read()
	cache.Store(snapshot, generation)
	return cloneSnapshot(snapshot)
}

func (s *Service) ensureCacheLocked() *catalogruntime.SnapshotCache[Snapshot] {
	if s.cache == nil {
		s.cache = catalogruntime.NewSnapshotCacheWithClock(s.ttl, cloneSnapshot, s.now)
	}
	return s.cache
}

func stateFromVerdict(
	def Definition,
	realization *Realization,
	got verdict,
	platform string,
	supportSource connectionSupportSource,
	checkedAt time.Time,
) State {
	prompt := def.prompt
	if !validPromptMode(prompt) {
		prompt = PromptOmit
	}
	connections := []Connection{}
	hostSupport := HostSupported
	if realization == nil {
		hostSupport = HostNotApplicable
	}
	if realization != nil {
		for _, template := range realization.Connections {
			target := template.Target
			if template.TargetFrom != "" {
				target = got.captures[template.TargetFrom]
			}
			connection := Connection{Mode: template.Mode, Transport: template.Transport, Target: target}
			if supportSource == nil || !supportSource(platform, connection) {
				hostSupport = HostUnsupported
			}
			if got.status == StatusAvailable {
				connections = append(connections, connection)
			}
		}
	}
	reason := got.reason
	if hostSupport == HostUnsupported {
		reason = "host_boundary_unsupported"
	}
	return State{
		ID: def.ID, Family: def.Family, Label: def.Label, Category: def.Category,
		Description: def.Description, DocsURL: def.DocsURL, Origin: def.origin,
		Status: got.status, HostSupport: hostSupport, Access: AccessAllow, AccessSetting: AccessSettingInherit,
		Prompt: prompt,
		Reason: reason, Surfaces: append([]ExecutionSurface(nil), def.Surfaces...), Connections: connections,
		CheckedAt: checkedAt,
	}
}

// ResolveForSurfaces requires every declared execution surface.
func (s *Service) ResolveForSurfaces(
	ctx context.Context,
	ids []string,
	project ProjectContext,
	surfaces []ExecutionSurface,
) (map[string]State, []string) {
	return ResolveIDs(s.SnapshotFor(ctx, false, project, nil), ids, surfaces)
}

// ResolveIDs filters a snapshot by id without taking another discovery pass.
func ResolveIDs(snapshot Snapshot, ids []string, surfaces []ExecutionSurface) (map[string]State, []string) {
	byID := make(map[string]State, len(snapshot.Resources))
	for _, state := range snapshot.Resources {
		byID[state.ID] = state
	}
	resolved := make(map[string]State, len(ids))
	var unmet []string
	seen := map[string]struct{}{}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		state, found := byID[id]
		if !found || state.Status != StatusAvailable || state.HostSupport != HostSupported ||
			(surfaces != nil && !SurfacesIncludeAll(surfaces, state.Surfaces)) {
			unmet = append(unmet, id)
			continue
		}
		resolved[id] = state
	}
	sort.Strings(unmet)
	return resolved, unmet
}

// SurfacesIncludeAll reports whether all required surfaces are available.
func SurfacesIncludeAll(available, required []ExecutionSurface) bool {
	have := make(map[ExecutionSurface]struct{}, len(available))
	for _, surface := range available {
		have[surface] = struct{}{}
	}
	for _, surface := range required {
		if _, ok := have[surface]; !ok {
			return false
		}
	}
	return true
}

// ResolveAction produces policy and concrete connection requests.
func (s *Service) ResolveAction(
	ctx context.Context,
	ids []string,
	project ProjectContext,
	surfaces []ExecutionSurface,
) (ActionResolution, []string) {
	resolved, unmet := s.ResolveForSurfaces(ctx, ids, project, surfaces)
	request := ConnectionRequest{}
	resolution := ActionResolution{States: resolved}
	for id, state := range resolved {
		resolution.Families = append(resolution.Families, state.Family)
		switch state.Access {
		case AccessAsk:
			resolution.Ask = append(resolution.Ask, id)
		case AccessDeny:
			resolution.Deny = append(resolution.Deny, id)
		case AccessAllow:
		}
		for _, connection := range state.Connections {
			switch connection.Mode {
			case ConnectionLocalService:
				if connection.Target != "" {
					request.LocalServices = append(request.LocalServices, LocalServiceEndpoint{
						Transport: connection.Transport,
						Target:    connection.Target,
					})
				}
			case ConnectionDirectIP:
				if connection.Target != "" {
					request.DirectDestinations = append(request.DirectDestinations, connection.Target)
				}
			case ConnectionNone, ConnectionProxy, ConnectionSOCKS, ConnectionBaselineLoopback, ConnectionDynamic:
			}
		}
	}
	request.LocalServices = uniqueSortedLocalServices(request.LocalServices)
	request.DirectDestinations = uniqueSorted(request.DirectDestinations)
	resolution.Ask = uniqueSorted(resolution.Ask)
	resolution.Deny = uniqueSorted(resolution.Deny)
	resolution.Families = uniqueSorted(resolution.Families)
	resolution.Connections = request
	// Declared order, not sorted: `resolved` is a map and the ids have already lost
	// their order by the time it exists. When two requested resources ship a program
	// of the same name, the one the caller asked for first is the one they meant.
	resolution.PathExtra = s.execPathExtra(ids, resolved)
	resolution.WriteRoots = s.realizationWriteRoots(ids, resolved)
	return resolution, unmet
}

func (s *Service) realizationWriteRoots(ids []string, resolved map[string]State) []string {
	if s == nil {
		return nil
	}
	byID := make(map[string]Definition, len(s.defs))
	for _, def := range s.defs {
		byID[def.ID] = def
	}
	var roots []string
	for _, id := range ids {
		if _, ok := resolved[id]; !ok {
			continue
		}
		def, ok := byID[id]
		if !ok {
			continue
		}
		realization := realizationForPlatform(def.Realizations, s.env.platform)
		if realization == nil {
			continue
		}
		for _, tmpl := range realization.WriteRoots {
			path, ok := expandPath(tmpl, s.env)
			if !ok {
				continue
			}
			roots = append(roots, path)
		}
	}
	return uniqueSorted(roots)
}

func uniqueSortedLocalServices(values []LocalServiceEndpoint) []LocalServiceEndpoint {
	seen := map[string]struct{}{}
	out := make([]LocalServiceEndpoint, 0, len(values))
	for _, value := range values {
		value.Target = strings.TrimSpace(value.Target)
		key := string(value.Transport) + "\x00" + value.Target
		if value.Target == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Transport != out[j].Transport {
			return out[i].Transport < out[j].Transport
		}
		return out[i].Target < out[j].Target
	})
	return out
}

func loadProjectGuidance(roots []string) (map[string]PromptMode, []Diagnostic) {
	merged := map[string]PromptMode{}
	var diagnostics []Diagnostic
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		path := filepath.Join(root, settingsoverlay.DirName(), "host-resources.yaml")
		data, err := readCatalogFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Code: "HOST_RESOURCES_PROJECT_GUIDANCE_READ_FAILED", Message: "Project host-resource guidance could not be read."})
			continue
		}
		catalog, err := ParseCatalog(data, "project")
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Code: "HOST_RESOURCES_PROJECT_GUIDANCE_INVALID", Message: err.Error()})
			continue
		}
		if len(catalog.Resources) > 0 {
			diagnostics = append(diagnostics, Diagnostic{Code: "HOST_RESOURCES_PROJECT_DEFINITION_FORBIDDEN", Message: "Project host-resource overlays may provide guidance only; device catalog definitions set discovery and routes."})
			continue
		}
		for id, mode := range catalog.Guidance {
			merged[id] = mode
		}
	}
	return merged, diagnostics
}

func snapshotFingerprint(snapshot Snapshot) string {
	parts := make([]string, 0, len(snapshot.Resources))
	for _, state := range snapshot.Resources {
		surfaces := make([]string, len(state.Surfaces))
		for i, surface := range state.Surfaces {
			surfaces[i] = string(surface)
		}
		sort.Strings(surfaces)
		parts = append(parts, strings.Join([]string{
			state.ID,
			state.Family,
			string(state.Status),
			string(state.HostSupport),
			string(state.Access),
			string(state.Prompt),
			strings.Join(surfaces, ","),
		}, "\x00"))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1e")))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func cloneSnapshot(in Snapshot) Snapshot {
	out := in
	out.Diagnostics = append([]Diagnostic(nil), in.Diagnostics...)
	out.Resources = make([]State, len(in.Resources))
	for i, state := range in.Resources {
		out.Resources[i] = state
		out.Resources[i].Surfaces = append([]ExecutionSurface(nil), state.Surfaces...)
		out.Resources[i].Connections = append([]Connection(nil), state.Connections...)
	}
	return out
}
