package searchadmin

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/search"
)

func (s *Handler) searchCompileContext(ctx context.Context, originProjectID string) search.CompileContext {
	return search.CompileContext{
		OriginProjectID:        originProjectID,
		ResolveProjectBySlug:   s.resolveSearchProjectSlug(ctx),
		RootsForProject:        s.rootsForSearchProject(ctx),
		AttachedProjectIDs:     s.attachedSearchProjectIDs(ctx),
		DependencyPathPatterns: sourceapi.SearchDependencyPatterns(),
	}
}

func (s *Handler) resolveSearchProjectSlug(ctx context.Context) func(slug string) (string, error) {
	return func(slug string) (string, error) {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			return "", fmt.Errorf("empty slug")
		}
		projects, err := s.projectRegistry.List(ctx)
		if err != nil {
			return "", err
		}
		for _, p := range projects {
			if searchProjectSlug(p) == slug {
				return p.ID, nil
			}
			for _, root := range p.Roots {
				if strings.EqualFold(root.Label, slug) {
					return p.ID, nil
				}
			}
			if strings.EqualFold(strings.TrimSpace(p.Name), slug) {
				return p.ID, nil
			}
		}
		return "", fmt.Errorf("unknown project slug")
	}
}

func (s *Handler) rootsForSearchProject(ctx context.Context) func(projectID string) ([]search.CodeRoot, error) {
	return func(projectID string) ([]search.CodeRoot, error) {
		p, err := s.projectRegistry.Get(ctx, strings.TrimSpace(projectID))
		if err != nil {
			return nil, err
		}
		var roots []search.CodeRoot
		for _, root := range p.Roots {
			if path := strings.TrimSpace(root.Path); path != "" {
				roots = append(roots, search.CodeRoot{RootID: root.ID, Path: path})
			}
		}
		return roots, nil
	}
}

func (s *Handler) attachedSearchProjectIDs(ctx context.Context) func() ([]string, error) {
	return func() ([]string, error) {
		projects, err := s.projectRegistry.List(ctx)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(projects))
		for _, p := range projects {
			if strings.TrimSpace(p.ID) != "" {
				ids = append(ids, p.ID)
			}
		}
		return ids, nil
	}
}

func (s *Handler) enrichSearchProjectNames(ctx context.Context, result *search.Result) {
	if result == nil {
		return
	}
	nameByID := map[string]string{}
	for i := range result.Hits {
		id := strings.TrimSpace(result.Hits[i].ProjectID)
		if id == "" {
			continue
		}
		name, ok := nameByID[id]
		if !ok {
			p, err := s.projectRegistry.Get(ctx, id)
			if err != nil {
				continue
			}
			name = searchProjectDisplayName(p)
			nameByID[id] = name
		}
		result.Hits[i].ProjectName = name
	}
}

// enrichSearchWorkerContext resolves child-session navigation coordinates.
func (s *Handler) enrichSearchWorkerContext(ctx context.Context, result *search.Result) {
	if result == nil || len(result.Hits) == 0 {
		return
	}
	type workerRef struct {
		parentSessionID string
		workerID        string
	}
	resolved := map[string]workerRef{}
	for i := range result.Hits {
		childSessionID := strings.TrimSpace(result.Hits[i].SessionID)
		if childSessionID == "" {
			continue
		}
		ref, seen := resolved[childSessionID]
		if !seen {
			var parent, worker string
			err := s.database.QueryRowContext(ctx,
				`SELECT parent_session_id, id FROM worker_jobs WHERE child_session_id = ? LIMIT 1`,
				childSessionID,
			).Scan(&parent, &worker)
			if err == nil {
				ref = workerRef{parentSessionID: strings.TrimSpace(parent), workerID: strings.TrimSpace(worker)}
			}
			resolved[childSessionID] = ref
		}
		result.Hits[i].ParentSessionID = ref.parentSessionID
		result.Hits[i].WorkerID = ref.workerID
	}
}

func searchProjectSlug(p project.Project) string {
	if name := strings.TrimSpace(p.Name); name != "" {
		return project.SlugProjectName(name)
	}
	for _, root := range p.Roots {
		if root.IsPrimary {
			return root.Label
		}
	}
	return ""
}

func searchProjectDisplayName(p *project.Project) string {
	if p == nil {
		return ""
	}
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	for _, root := range p.Roots {
		if root.IsPrimary {
			return root.Label
		}
	}
	return p.ID
}

// searchService reads the evidence index through the host database.
func (s *Handler) searchService() *search.Service {
	return search.NewService(s.database, s.rerank, s.symbolExecutor)
}
