// Package browsertestsetup enables a pinned browser for test binaries.
package browsertestsetup

import (
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testsetup/enginebundle"
)

// Enable selects the staged browser or the developer's pinned cache.
func Enable() {
	if strings.TrimSpace(os.Getenv(browserengine.EnvBrowserBin)) != "" {
		return
	}
	bin := browserengine.BundledBinary(enginebundle.Root())
	if home := strings.TrimSpace(os.Getenv("PW_TEST_HOST_HOME")); bin == "" && home != "" {
		cache := enginepaths.BrowserCacheRootUnder(configdir.ChannelConfigRoot(home))
		bin = browserengine.ManagedBinary(cache)
	}
	if bin != "" {
		_ = os.Setenv(browserengine.EnvBrowserBin, bin)
	}
}
