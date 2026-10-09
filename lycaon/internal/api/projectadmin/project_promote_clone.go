package projectadmin

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// HandlePromoteProject records a durable save-to-folder intent and resumes it.
func (s *Promotion) HandlePromoteProject(w http.ResponseWriter, r *http.Request) {

	id := strings.TrimSpace(chi.URLParam(r, "id"))
	var req wire.PromoteProjectRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.RootPath) == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "root_path is required")
		return
	}

	before, err := s.Registry.Get(r.Context(), id)
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return
	}
	if before.Promotion == nil {
		if _, rootErr := project.DraftPromotionRoot(before); rootErr != nil {
			s.responses.ProjectRegistryError(w, r, rootErr)
			return
		}
	} else {
		if !project.SamePath(before.Promotion.DestinationPath, req.RootPath) || before.Promotion.InitGit != req.InitGit {
			s.responses.Fail(w, wire.ApiErrorCodePromotionConflict, "a save to folder with different settings is already queued for this draft")
			return
		}
	}

	engine := s.PromotionEngine()
	if before.Promotion == nil {
		if _, err := engine.Create(r.Context(), id, req.RootPath, req.InitGit); err != nil {
			if errors.Is(err, project.ErrPromotionDestinationNotEmpty) {
				s.responses.Fail(w, wire.ApiErrorCodeFolderNotEmpty, "the folder must be empty — choose an empty folder or create a new one")
				return
			}
			s.responses.ProjectRegistryError(w, r, err)
			return
		}
	}
	queued, err := s.Registry.Get(r.Context(), id)
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return
	}

	quiescent, quiescentErr := s.Sessions.ProjectControl.ProjectPromoteQuiescent(r.Context(), id)
	if quiescentErr != nil {
		s.responses.InternalError(w, r, quiescentErr)
		return
	}

	if !quiescent {
		s.Projects.publishProjectLifecycleEvent(r.Context(), wire.ProjectEventUpdated, queued)
		httpio.WriteJSON(w, http.StatusAccepted, project.ToAPI(queued))
		return
	}

	p, promoteErr := s.executeDraftPromote(r.Context(), id, req.RootPath, req.InitGit)
	if promoteErr != nil {
		if errors.Is(promoteErr, project.ErrPromotionDestinationNotEmpty) {
			s.responses.Fail(w, wire.ApiErrorCodeFolderNotEmpty, "the folder must be empty — choose an empty folder or create a new one")
			return
		}
		if errors.Is(promoteErr, project.ErrMutationInProgress) || errors.Is(promoteErr, project.ErrProjectBusy) {
			s.Projects.publishProjectLifecycleEvent(r.Context(), wire.ProjectEventUpdated, queued)
			httpio.WriteJSON(w, http.StatusAccepted, project.ToAPI(queued))
			return
		}
		s.responses.ProjectRegistryError(w, r, promoteErr)
		return
	}
	s.Projects.publishProjectLifecycleEvent(r.Context(), wire.ProjectEventUpdated, p)
	httpio.WriteJSON(w, http.StatusCreated, project.ToAPI(p))
}

// HandleCloneProject clones a remote into a new folder under parent_dir and opens
// it as a saved project.
func (s *Promotion) HandleCloneProject(w http.ResponseWriter, r *http.Request) {

	var req wire.CloneProjectRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	url := strings.TrimSpace(req.URL)
	if url == "" || strings.HasPrefix(url, "-") {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "a repository url is required")
		return
	}
	parent, err := project.ResolveExistingDir(req.ParentDir)
	if err != nil {
		s.responses.PathError(w, r, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = repoNameFromURL(url)
	}
	if name == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "could not derive a folder name from the url")
		return
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) || filepath.IsAbs(name) {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "name must be a single folder name")
		return
	}
	dest := filepath.Join(parent, name)

	if cloneErr := s.Git.Manager().Clone(r.Context(), url, dest); cloneErr != nil {
		if errors.Is(cloneErr, git.ErrCloneDestinationExists) {
			s.responses.Fail(w, wire.ApiErrorCodeFolderExists, "a folder with that name already exists at the destination")
			return
		}
		s.responses.Logger.WarnContext(r.Context(), "project clone failed", "err", cloneErr)
		s.responses.Fail(w, wire.ApiErrorCodeCloneFailed, "git clone failed")
		return
	}

	p, err := s.Registry.Create(r.Context(), project.CreateParams{
		Name:  name,
		Roots: []project.AttachRootParams{{Path: dest}},
	})
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return
	}
	for _, root := range p.Roots {
		s.Roots.afterRootAttached(r.Context(), p.ID, root.Path)
	}
	s.Verification.detectVerifyAsync(r.Context(), p.ID)
	s.Projects.publishProjectLifecycleEvent(r.Context(), wire.ProjectEventCreated, p)
	httpio.WriteJSON(w, http.StatusCreated, project.ToAPI(p))
}

// repoNameFromURL derives a folder name from a clone url (basename minus ".git").
func repoNameFromURL(url string) string {
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		url = url[i+1:]
	}
	return strings.TrimSuffix(url, ".git")
}
