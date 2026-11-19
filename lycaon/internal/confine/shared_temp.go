package confine

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/fspath"
)

// SharedTempLocations returns the canonical machine temp directories.
func SharedTempLocations() []string {
	var locations []string
	for _, path := range []string{"/tmp", "/var/tmp", os.TempDir(), platformUserTemp()} {
		if !filepath.IsAbs(path) {
			continue
		}
		location := fspath.CanonicalPath(path)
		if location != "" && filepath.Dir(location) != location && !slices.Contains(locations, location) {
			locations = append(locations, location)
		}
	}
	return locations
}

// SharedTempLocation selects the narrowest location covering path.
func SharedTempLocation(path string) string {
	if !filepath.IsAbs(path) {
		return ""
	}
	path = fspath.CanonicalPath(path)
	var best string
	for _, location := range SharedTempLocations() {
		if len(location) > len(best) && PathAtOrUnder(path, location) {
			best = location
		}
	}
	return best
}
