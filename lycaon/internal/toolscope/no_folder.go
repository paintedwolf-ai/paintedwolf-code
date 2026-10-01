package toolscope

import (
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
)

// NoFolderAllowlist returns the tools callable with no folder attached, sorted.
func NoFolderAllowlist() ([]string, error) {
	catalog, err := surfacecatalog.Load()
	if err != nil {
		return nil, err
	}
	return catalog.NoFolderAllowlist(), nil
}

// AllowedWithNoFolder reports whether name is on the catalog allowlist.
func AllowedWithNoFolder(name string) bool {
	names, err := NoFolderAllowlist()
	if err != nil {
		return false
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for _, candidate := range names {
		if strings.ToLower(candidate) == want {
			return true
		}
	}
	return false
}

// RequiresProjectRoots reports whether name needs an attached folder.
func RequiresProjectRoots(name string) bool {
	return !AllowedWithNoFolder(name)
}
