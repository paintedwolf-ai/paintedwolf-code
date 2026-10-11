package projectadmin

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var projectListBounds = httpio.MustPageLimit(100, 1, 200)

type projectPosition struct {
	LastOpenedAt time.Time `json:"last_opened_at"`
	ID           string    `json:"id"`
}

var projectPages = pagecursor.For[projectPosition]("project_list")

func (s *Projects) HandleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req wire.CreateProjectRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	params := project.CreateParams{Draft: req.Draft}
	if req.Name != nil {
		params.Name = strings.TrimSpace(*req.Name)
	}
	for _, rootIn := range req.Roots {
		params.Roots = append(params.Roots, project.AttachRootParams{
			Path:      rootIn.Path,
			Label:     rootIn.Label,
			IsPrimary: rootIn.IsPrimary,
		})
	}
	p, err := s.Registry.Create(r.Context(), params)
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return
	}
	for _, root := range p.Roots {
		if s.sourceWorkspace.SourceLedger != nil && s.sourceWorkspace.SourceLedger.Snapshots != nil {
			if err := s.sourceWorkspace.SourceLedger.Snapshots.DiscardObservations(r.Context(), root.Path); err != nil {
				slog.WarnContext(r.Context(), "discard stale source observations", "path", root.Path, "err", err)
			}
		}
		s.Roots.afterRootAttached(r.Context(), p.ID, root.Path)
	}
	s.Verification.detectVerifyAsync(r.Context(), p.ID)
	// Contributions and workflows are read next, and both resolve this
	// project's catalog for the first time.
	s.Extensions.WarmEffectiveCatalog(context.WithoutCancel(r.Context()), p.ID)
	s.publishProjectLifecycleEvent(r.Context(), wire.ProjectEventCreated, p)
	httpio.WriteJSON(w, http.StatusCreated, project.ToAPI(p))
}

func (s *Projects) HandleGetProject(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Registry, s.responses, w, r)
	if !ok {
		return
	}
	httpio.WriteJSON(w, http.StatusOK, project.ToAPI(p))
}

func (s *Projects) HandleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	var req wire.UpdateProjectRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	p, err := s.Registry.Patch(r.Context(), id, project.PatchParams{
		Name:    req.Name,
		Starred: req.Starred,
	})
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return
	}
	s.publishProjectLifecycleEvent(r.Context(), wire.ProjectEventUpdated, p)
	httpio.WriteJSON(w, http.StatusOK, project.ToAPI(p))
}

func (s *Projects) publishProjectLifecycleEvent(ctx context.Context, action wire.ProjectEventAction, p *project.Project) {
	projectview.PublishEvent(s.Registry, s.Events, ctx, action, p)
}

func (s *Projects) HandleListProjects(w http.ResponseWriter, r *http.Request) {
	query, err := httpio.ReadPageQuery(r, projectListBounds)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	projects, err := s.Registry.List(r.Context())
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}

	startIndex := 0
	if query.Cursor != "" {
		after, err := projectPages.Decode(query.Cursor, "")
		if err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		startIndex = len(projects)
		found := false
		for i := range projects {
			if projects[i].ID == after.ID {
				startIndex = i + 1
				found = true
				break
			}
		}
		if !found {
			for i := range projects {
				if projects[i].LastOpenedAt.Before(after.LastOpenedAt) {
					startIndex = i
					break
				}
			}
		}
	}

	endIndex := startIndex + query.Limit
	hasMore := false
	if endIndex < len(projects) {
		hasMore = true
	} else {
		endIndex = len(projects)
	}

	var pageSlice []project.Project
	if startIndex < len(projects) {
		pageSlice = projects[startIndex:endIndex]
	}

	out := make([]wire.Project, 0, len(pageSlice))
	for i := range pageSlice {
		out = append(out, project.ToAPI(&pageSlice[i]))
	}

	var nextCursor string
	if hasMore && len(pageSlice) > 0 {
		last := &pageSlice[len(pageSlice)-1]
		token, err := projectPages.Encode("", projectPosition{
			LastOpenedAt: last.LastOpenedAt,
			ID:           last.ID,
		})
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		nextCursor = token
	}

	httpio.WriteJSON(w, http.StatusOK, wire.ProjectListResponse{Projects: out, NextCursor: nextCursor})
}
