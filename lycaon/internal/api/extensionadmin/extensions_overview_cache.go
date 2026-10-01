package extensionadmin

import (
	"sync"

	"github.com/lycaon/lycaon/internal/extpacks"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// extensionsOverviewCache holds one overview per scope, keyed by overviewStamp.
type extensionsOverviewCache struct {
	mu         sync.Mutex
	projectDir string
	stamp      string
	value      wire.ExtensionsCatalogView
}

func (c *extensionsOverviewCache) get(projectDir string) (wire.ExtensionsCatalogView, bool) {
	stamp := overviewStamp(projectDir)
	c.mu.Lock()
	defer c.mu.Unlock()
	if stamp == "" || c.stamp != stamp || c.projectDir != projectDir {
		return wire.ExtensionsCatalogView{}, false
	}
	return c.value, true
}

func (c *extensionsOverviewCache) put(projectDir string, out wire.ExtensionsCatalogView) {
	stamp := overviewStamp(projectDir)
	if stamp == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.projectDir, c.stamp, c.value = projectDir, stamp, out
}

// overviewStamp identifies every input of the overview: desired state and the
// installed meta-packs.
func overviewStamp(projectDir string) string {
	desired := extpacks.DesiredStamp([]string{projectDir})
	if desired == "" {
		return ""
	}
	return desired + "|" + extpacks.MetaPackCacheStamp()
}
