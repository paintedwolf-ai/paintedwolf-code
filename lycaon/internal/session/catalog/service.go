package catalog

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/workscope"
	"github.com/lycaon/lycaon/pkg/api"
	"golang.org/x/sync/singleflight"
)

type SessionReader interface {
	Get(context.Context, string) (*api.Session, error)
}
type Service struct {
	work                    workscope.Group
	lifecycleMu             sync.Mutex
	stopped                 bool
	releaseRefresher        func()
	store                   SessionReader
	projects                project.Registry
	catalogMu               sync.RWMutex
	effectiveCatalogCache   scopedstore.LRU[effectiveCatalogCacheEntry]
	effectiveCatalogResolve singleflight.Group
	catalogModuleRoot       string
	bootCatalog             *extpacks.EffectiveCatalog
	trustSurfaces           *settings.TrustSurfacesStore
	viewCache               *catalogview.Cache
	viewFailLog             scopedstore.LRU[struct{}]
}

func New(store SessionReader) Service                    { return Service{store: store} }
func (m *Service) SetProjects(registry project.Registry) { m.projects = registry }
func (m *Service) Configure(moduleRoot string, boot *extpacks.EffectiveCatalog, surfaces *settings.TrustSurfacesStore) {
	if m == nil {
		return
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.stopped {
		return
	}
	if m.releaseRefresher != nil {
		m.releaseRefresher()
	}
	m.catalogModuleRoot = strings.TrimSpace(moduleRoot)
	m.bootCatalog = boot
	m.trustSurfaces = surfaces
	m.releaseRefresher = extpacks.SetActiveRefresher(m.reinstallActive)
}

// Stop cancels detached resolutions and prevents new catalog work.
func (m *Service) Stop() {
	m.lifecycleMu.Lock()
	m.stopped = true
	if m.releaseRefresher != nil {
		m.releaseRefresher()
		m.releaseRefresher = nil
	}
	m.lifecycleMu.Unlock()
	m.work.Stop()
}

// Wait joins resolutions before their device dependencies are released.
func (m *Service) Wait(ctx context.Context) error { return m.work.Wait(ctx) }
