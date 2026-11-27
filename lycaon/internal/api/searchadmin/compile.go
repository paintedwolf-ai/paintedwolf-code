package searchadmin

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourcebranch"
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
// When r is non-nil (interactive HandleSearch), the code leg overlays dirty
// editor buffers visible to the authorized caller and workspace resolved from r
// (never used for exports or unauthorized callers).
func (s *Handler) searchService(r *http.Request) *search.Service {
	var drafts search.CodeDraftProvider
	if r != nil && s.editorDocuments != nil {
		drafts = s.authorizedCodeDrafts(r)
	}
	return search.NewServiceWithDrafts(s.database, s.rerank, s.symbolExecutor, drafts)
}

// authorizedCodeDrafts resolves dirty editor drafts strictly from the
// authorized caller and workspace scope of r (matching normal editor scope
// admission via requestscope.ResolveSessionProject). A project ID or root ID
// alone never authorizes reading a dirty draft, callers acting in another
// workspace/worktree do not see trunk drafts, and queries are bounded to one
// (projectID, branch, rootID).
func (s *Handler) authorizedCodeDrafts(r *http.Request) search.CodeDraftProvider {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	return func(ctx context.Context, projectID, rootID string, maxDocs int, maxBytes int64) ([]search.CodeDraft, error) {
		caller, ok := people.Caller(ctx)
		if !ok || !people.MayInvoke(caller, "openEditorDocument") {
			return nil, nil
		}
		p, err := s.projectRegistry.Get(ctx, strings.TrimSpace(projectID))
		if err != nil || p == nil {
			return nil, nil
		}
		if sessionID != "" {
			if s.sessionStore == nil {
				return nil, nil
			}
			scoped, err := requestscope.ResolveSessionProject(s.sessionStore, ctx, p, sessionID)
			if err != nil {
				// Invalid, cross-project, or stale workspace session never falls back to trunk drafts.
				return nil, nil
			}
			// A session bound to a separate worktree workspace does not share trunk drafts.
			if scoped.Binding != nil || scoped.Project.WorkspaceID() != p.WorkspaceID() {
				return nil, nil
			}
			p = scoped.Project
		}
		branch := p.BranchForRoot(rootID)
		if branch != sourcebranch.Trunk {
			return nil, nil
		}
		docs, err := s.editorDocuments.DirtyDraftsForRoot(ctx, p.ID, branch, rootID, maxDocs, maxBytes)
		if err != nil || len(docs) == 0 {
			return nil, err
		}
		out := make([]search.CodeDraft, 0, len(docs))
		for _, d := range docs {
			out = append(out, search.CodeDraft{Path: d.Path, Content: d.Draft})
		}
		return out, nil
	}
}
