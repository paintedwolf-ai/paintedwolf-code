// Package projectcontrib inventories project-supplied agent configuration.
package projectcontrib

import (
	"strings"

	"github.com/lycaon/lycaon/internal/governance"
)

// Item is one contributed file, server, or pack.
type Item struct {
	Name     string `json:"name"`
	Detail   string `json:"detail,omitempty"`
	RootPath string `json:"-"`
	// Path is root-relative for file items.
	Path string `json:"path,omitempty"`
	// Lines is a prose size hint.
	Lines int `json:"lines,omitempty"`
}

// File retains the exact bytes used by a contribution scan.
type File struct {
	RootPath string
	Path     string
	Content  string
	SHA256   string
}

// Surface includes captured files and their display metadata.
type Surface struct {
	ID            string `json:"id"`
	Files         []File `json:"-"`
	CapturedBytes int    `json:"-"`
	ReadError     error  `json:"-"`
	Items         []Item `json:"items"`
	Count         int    `json:"count"`
}

// Manifest is everything a project supplies, discovered from its roots.
type Manifest struct {
	Surfaces []Surface `json:"surfaces"`
}

// Surface returns the discovered surface by id.
func (m Manifest) Surface(id string) (Surface, bool) {
	for _, s := range m.Surfaces {
		if s.ID == id {
			return s, true
		}
	}
	return Surface{}, false
}

func scanSurface(surfaceID string, roots []string, indices map[string][]governance.ResolvedAgentsMD) Surface {
	switch surfaceID {
	case SurfaceAgentsMD:
		return scanGuidanceIndex(roots, indices)
	case SurfaceSkills:
		return scanSkills(roots)
	case SurfaceProjectSettings:
		return scanProjectSettings(roots)
	case SurfaceProjectMCP:
		return scanProjectMCP(roots)
	case SurfaceScanConfig:
		return scanScanConfig(roots)
	case SurfacePromptOverrides:
		return scanPromptOverrides(roots)
	case SurfaceExtensionConfig:
		return scanExtensionConfig(roots)
	case SurfaceExtensionSuggestions:
		return scanExtensionSuggestions(roots)
	default:
		return Surface{}
	}
}

func cleanRoots(rootPaths []string) []string {
	out := make([]string, 0, len(rootPaths))
	seen := make(map[string]struct{}, len(rootPaths))
	for _, p := range rootPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
