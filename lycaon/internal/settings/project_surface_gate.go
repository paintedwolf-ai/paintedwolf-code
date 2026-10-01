package settings

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
)

// ProjectLookup is the narrow project-registry port the overlay gate needs.
type ProjectLookup interface {
	Get(ctx context.Context, id string) (*project.Project, error)
	List(ctx context.Context) ([]project.Project, error)
}

// ProjectSurfaceGate checks whether project settings may be merged.
type ProjectSurfaceGate struct {
	Surface  string
	Surfaces *TrustSurfacesStore
	Projects ProjectLookup
}

// Applies reports whether projectID's tree settings may be merged.
func (g *ProjectSurfaceGate) Applies(ctx context.Context, projectID string) bool {
	if g == nil || g.Surfaces == nil || g.Projects == nil || g.Surface == "" {
		return false
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return false
	}
	p, err := g.Projects.Get(ctx, projectID)
	if err != nil || p == nil {
		return false
	}
	return g.Surfaces.Applies(g.Surface, *p)
}

// Dir returns projectDir when the project's tree settings apply, "" otherwise.
// Stores keyed by directory take this in place of a raw workspace path.
func (g *ProjectSurfaceGate) Dir(ctx context.Context, projectID, projectDir string) string {
	if !g.Applies(ctx, projectID) {
		return ""
	}
	return projectDir
}

// AppliesPath resolves a path through its most specific project root.
func (g *ProjectSurfaceGate) AppliesPath(ctx context.Context, rootPath string) bool {
	if g == nil || g.Surfaces == nil || g.Projects == nil || g.Surface == "" {
		return false
	}
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return false
	}
	target := filepath.Clean(rootPath)
	projects, err := g.Projects.List(ctx)
	if err != nil {
		return false
	}
	containingProject, ok := projectForPath(projects, target)
	if !ok {
		return false
	}
	return g.Surfaces.Applies(g.Surface, containingProject)
}

// projectForPath returns the project with the most specific containing root.
func projectForPath(projects []project.Project, target string) (project.Project, bool) {
	best := -1
	var containingProject project.Project
	tied := false
	for _, p := range projects {
		match := -1
		for _, root := range p.Roots {
			dir := filepath.Clean(strings.TrimSpace(root.Path))
			if dir == "." || dir == "" {
				continue
			}
			if !pathUnderRoot(target, dir) {
				continue
			}
			// Both roots contain target, so the longer string is the deeper directory.
			if len(dir) > match {
				match = len(dir)
			}
		}
		if match < 0 {
			continue
		}
		switch {
		case match > best:
			best, containingProject, tied = match, p, false
		case match == best:
			tied = true
		}
	}
	if best < 0 || tied {
		return project.Project{}, false
	}
	return containingProject, true
}

// pathUnderRoot follows the project open policy's case folding.
func pathUnderRoot(target, root string) bool {
	t := strings.ToLower(target)
	r := strings.ToLower(root)
	if t == r {
		return true
	}
	if !strings.HasSuffix(r, string(filepath.Separator)) {
		r += string(filepath.Separator)
	}
	return strings.HasPrefix(t, r)
}

// FilterPaths keeps only root paths whose project has this surface applying.
func (g *ProjectSurfaceGate) FilterPaths(ctx context.Context, rootPaths []string) []string {
	if len(rootPaths) == 0 {
		return nil
	}
	out := make([]string, 0, len(rootPaths))
	for _, p := range rootPaths {
		if g.AppliesPath(ctx, p) {
			out = append(out, p)
		}
	}
	return out
}
