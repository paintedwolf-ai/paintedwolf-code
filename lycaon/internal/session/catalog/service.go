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
	"github.com/lycaon/lycaon/pkg/api"
	"golang.org/x/sync/singleflight"
)

type SessionReader interface {
	Get(context.Context, string) (*api.Session, error)
}
type Service struct {
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
	m.catalogModuleRoot = strings.TrimSpace(moduleRoot)
	m.bootCatalog = boot
	m.trustSurfaces = surfaces
	extpacks.SetActiveRefresher(m.reinstallActive)
}
