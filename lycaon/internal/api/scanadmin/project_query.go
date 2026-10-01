package scanadmin

import (
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// requireProjectScanPaths resolves {id} and the canonical root paths whose scans it lists.
func (s *Handler) requireProjectScanPaths(w http.ResponseWriter, r *http.Request) (projectID string, canonicalPaths []string, ok bool) {
	p, ok := requestscope.ProjectByURLID(s.Projects, s.responses, w, r)
	if !ok {
		return "", nil, false
	}
	roots, ok := s.RequireScanRoots(w, r, p, true)
	return p.ID, roots, ok
}

// Root selection is an attached identity, never a caller-supplied path.
func (s *Handler) RequireScanRoots(w http.ResponseWriter, r *http.Request, p *project.Project, allByDefault bool) ([]string, bool) {
	id := strings.TrimSpace(r.URL.Query().Get("root_id"))
	if id != "" {
		for _, root := range p.Roots {
			if root.ID == id {
				return []string{root.Path}, true
			}
		}
		s.responses.Fail(w, wire.ApiErrorCodeRootNotFound, "the selected folder is no longer attached to this project")
		return nil, false
	}
	if allByDefault {
		return project.RootPaths(p), true
	}
	if root := project.PrimaryRootPath(p); root != "" {
		return []string{root}, true
	}
	s.responses.Fail(w, wire.ApiErrorCodeNoProjectRoot, "project has no attached root to scan")
	return nil, false
}
