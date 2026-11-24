package surface

import "github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"

// AllowsWatchManagement reports whether surfaceID admits watch resource controls.
func AllowsWatchManagement(surfaceID string) bool {
	cat, err := surfacecatalog.Load()
	if err != nil {
		return false
	}
	surf, err := cat.Surface(surfaceID)
	if err != nil {
		return false
	}
	return surf.WatchManagement
}
