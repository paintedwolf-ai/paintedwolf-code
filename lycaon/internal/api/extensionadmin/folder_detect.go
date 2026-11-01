package extensionadmin

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Suggestions) HandleDetectFolder(w http.ResponseWriter, r *http.Request) {
	folder, err := project.ResolveExistingDir(r.URL.Query().Get("path"))
	if err != nil {
		s.responses.PathError(w, r, err)
		return
	}
	out := wire.FolderDetect{Path: folder}
	matches := requestscope.ProjectsByRootPath(s.Projects, r.Context(), folder)
	var singleKnown *project.Project
	if len(matches) > 0 {
		out.Projects = make([]wire.FolderDetectProject, 0, len(matches))
		for i := range matches {
			out.Projects = append(out.Projects, project.ToFolderDetectProject(&matches[i], folder))
			if singleKnown == nil && project.IsSingleRootForPath(&matches[i], folder) {
				singleKnown = &matches[i]
			}
		}
	}
	if singleKnown != nil {
		out.ProjectID = singleKnown.ID
	}
	out.ExtensionSuggestions = s.folderExtensionSuggestions(r, folder, singleKnown)
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *Suggestions) folderExtensionSuggestions(
	r *http.Request,
	folder string,
	known *project.Project,
) *wire.ExtensionSuggestionsResponse {
	subject := known
	if subject == nil {
		subject = &project.Project{Roots: []project.Root{{Path: folder}}}
	}
	if !s.Settings.TrustSurfaces.Applies(projectcontrib.SurfaceExtensionSuggestions, *subject) {
		return nil
	}
	manifest, err := extpacks.LoadSuggestionFile(extpacks.ProjectDesiredPath(folder))
	if err != nil || len(manifest.Suggest) == 0 {
		return nil
	}
	response, err := s.extensionSuggestionsResponse(r, subject.ID, manifest)
	if err != nil || len(response.Suggestions) == 0 {
		return nil
	}
	return &response
}
