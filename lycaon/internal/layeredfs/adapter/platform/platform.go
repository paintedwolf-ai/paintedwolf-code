// Package platform binds the adapter contract to the platform adapters at the engine's
// composition root, so consumers never import one or branch on the operating system.
package platform

import (
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
)

// ConnectionRecoveries names the adapters able to settle connections left by an earlier
// engine process. A mounted adapter absent here settles only once its mount is gone.
func ConnectionRecoveries() map[adapter.AdapterID]adapter.ConnectionRecovery {
	return map[adapter.AdapterID]adapter.ConnectionRecovery{
		adapter.AdapterFUSELinux: fuselinux.New(fuselinux.Config{}),
	}
}
