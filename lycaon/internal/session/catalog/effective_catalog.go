package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/pkg/api"
)

type effectiveCatalogCacheEntry struct {
	catalog *extpacks.EffectiveCatalog
	key     string
}

// InvalidateEffectiveCatalog drops cached catalogs. Empty projectID clears all.
func (m *Service) InvalidateEffectiveCatalog(projectID string) {
	if m == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()
	if projectID == "" {
		m.effectiveCatalogCache.Clear()
		return
	}
	for cacheID := range m.effectiveCatalogCache.Snapshot() {
		if cacheID == projectID || strings.HasPrefix(cacheID, projectID+"\x00") {
			m.effectiveCatalogCache.Delete(cacheID)
		}
	}
}

// EffectiveCatalogForProject returns the resolved catalog for projectID.
// Projects with Extension settings off use the device catalog.
func (m *Service) EffectiveCatalogForProject(ctx context.Context, projectID string) (*extpacks.EffectiveCatalog, error) {
	if m == nil {
		return extpacks.Active(), nil
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return m.DeviceCatalog(ctx), nil
	}
	if m.projects == nil {
		return m.DeviceCatalog(ctx), nil
	}
	p, err := m.projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return m.effectiveCatalogForProject(ctx, p)
}

func (m *Service) effectiveCatalogForProject(ctx context.Context, p *project.Project) (*extpacks.EffectiveCatalog, error) {
	if p == nil {
		return m.DeviceCatalog(ctx), nil
	}
	return m.effectiveCatalogForWorkspace(ctx, p, "")
}

func (m *Service) effectiveCatalogForWorkspace(ctx context.Context, p *project.Project, workspaceRootID string) (*extpacks.EffectiveCatalog, error) {
	overlay, err := project.ResolveProjectOverlay(p, workspaceRootID)
	if err != nil {
		return nil, err
	}
	if err := overlay.CheckCompatibility(); err != nil {
		return nil, err
	}
	projectDirs := overlay.Paths
	key := m.catalogCacheKey(*p, projectDirs)
	cacheID := p.ID
	if workspaceRootID = strings.TrimSpace(workspaceRootID); workspaceRootID != "" {
		cacheID += "\x00" + workspaceRootID
	}
	m.catalogMu.RLock()
	if ent, ok := m.effectiveCatalogCache.Load(cacheID); ok && ent.key == key {
		c := ent.catalog
		m.catalogMu.RUnlock()
		return c, nil
	}
	m.catalogMu.RUnlock()

	resolveResult := m.effectiveCatalogResolve.DoChan(cacheID+"\x00"+key, func() (any, error) {
		m.catalogMu.RLock()
		if ent, ok := m.effectiveCatalogCache.Load(cacheID); ok && ent.key == key {
			m.catalogMu.RUnlock()
			return ent.catalog, nil
		}
		m.catalogMu.RUnlock()
		catalog, resolveErr := m.resolveEffectiveCatalogForRoots(context.WithoutCancel(ctx), *p, projectDirs)
		if resolveErr != nil {
			return nil, resolveErr
		}
		m.catalogMu.Lock()
		m.effectiveCatalogCache.Store(cacheID, effectiveCatalogCacheEntry{catalog: catalog, key: key})
		m.catalogMu.Unlock()
		return catalog, nil
	})
	var resolved any
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resolveResult:
		if result.Err != nil {
			return nil, result.Err
		}
		resolved = result.Val
	}
	catalog, ok := resolved.(*extpacks.EffectiveCatalog)
	if !ok {
		return nil, fmt.Errorf("effective catalog resolver returned %T", resolved)
	}
	return catalog, nil
}

func (m *Service) catalogCacheKey(p project.Project, projectDirs []string) string {
	return fmt.Sprintf("%d:%t:%s",
		p.RootsGeneration,
		project.TrustEnabled(p, projectcontrib.SurfaceExtensionConfig),
		extpacks.DesiredStamp(projectDirs))
}

func (m *Service) resolveEffectiveCatalogForRoots(ctx context.Context, p project.Project, projectDirs []string) (*extpacks.EffectiveCatalog, error) {
	root := m.catalogModuleRoot
	if root == "" {
		return m.DeviceCatalog(ctx), nil
	}
	m.refreshActiveIfStale(ctx)
	// Only trusted project disables affect the runtime catalog.
	resolveDirs := []string(nil)
	if m.trustSurfaces != nil &&
		m.trustSurfaces.Applies(projectcontrib.SurfaceExtensionConfig, p) {
		resolveDirs = append(resolveDirs, projectDirs...)
	}
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		return nil, err
	}
	scanners := scan.RequirementChecker{ModuleRoot: root, HomeDir: homeDir}
	eff, err := extpacks.ResolveCatalog(ctx, resolveDirs, scanners)
	if err != nil {
		return nil, err
	}
	// Cache the committed view when available.
	if m.viewCache != nil {
		if _, committed, err := m.viewCache.ForCommitted(ctx, eff); err == nil {
			eff = committed
		}
	}
	return eff, nil
}

func (m *Service) effectiveCatalogForSession(ctx context.Context, sess *api.Session) (*extpacks.EffectiveCatalog, error) {
	if sess == nil || strings.TrimSpace(sess.ProjectID) == "" || m.projects == nil {
		return m.DeviceCatalog(ctx), nil
	}
	p, err := m.projects.Get(ctx, sess.ProjectID)
	if err != nil {
		return nil, err
	}
	return m.effectiveCatalogForWorkspace(ctx, p, sess.WorkspaceRootID)
}

// DeviceCatalog returns the current device catalog.
func (m *Service) DeviceCatalog(ctx context.Context) *extpacks.EffectiveCatalog {
	m.refreshActiveIfStale(ctx)
	if active := extpacks.Active(); active != nil {
		return active
	}
	if m.bootCatalog != nil {
		return m.bootCatalog
	}
	root := m.catalogModuleRoot
	if root == "" {
		return nil
	}
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		return nil
	}
	scanners := scan.RequirementChecker{ModuleRoot: root, HomeDir: homeDir}
	eff, err := extpacks.ResolveCatalog(ctx, nil, scanners)
	if err != nil {
		return nil
	}
	return eff
}

// refreshActiveIfStale refreshes device state changed outside the API.
func (m *Service) refreshActiveIfStale(ctx context.Context) {
	if m == nil || !extpacks.ActiveIsStale() {
		return
	}
	m.reinstallActive(ctx)
}

// reinstallActive resolves and publishes committed device state. It is the
// refresher extpacks.RefreshActiveIfStale runs.
func (m *Service) reinstallActive(ctx context.Context) {
	if m == nil {
		return
	}
	root := strings.TrimSpace(m.catalogModuleRoot)
	if root == "" {
		return
	}
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		return
	}
	scanners := scan.RequirementChecker{ModuleRoot: root, HomeDir: homeDir}
	if _, err := extpacks.ApplyCatalog(ctx, nil, scanners); err != nil {
		slog.WarnContext(ctx, "extensions: device desired state changed on disk but does not resolve; keeping the previous catalog",
			"error", err)
	}
}

// CatalogForProjectDir returns a CatalogFor closure for workflow.ManifestResolver.
func (m *Service) CatalogForProjectDir(projectID string) func(ctx context.Context, projectDir, sessionID string) *extpacks.EffectiveCatalog {
	projectID = strings.TrimSpace(projectID)
	return func(ctx context.Context, _ string, sessionID string) *extpacks.EffectiveCatalog {
		if m == nil {
			return extpacks.Active()
		}
		if projectID != "" {
			if c, err := m.EffectiveCatalogForProject(ctx, projectID); err == nil && c != nil {
				return c
			}
		}
		if sessionID != "" && m.store != nil {
			if sess, err := m.store.Get(ctx, sessionID); err == nil && sess != nil && strings.TrimSpace(sess.ProjectID) != "" {
				if c, err := m.EffectiveCatalogForProject(ctx, sess.ProjectID); err == nil && c != nil {
					return c
				}
			}
		}
		return m.DeviceCatalog(ctx)
	}
}
