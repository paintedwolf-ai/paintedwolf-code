package sourceapi

import (
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *History) HandleGetProjectSourceCommandObservation(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	chat, ok := requestscope.Session(s.SessionStore, s.responses, w, r, r.URL.Query().Get("session_id"))
	if !ok {
		return
	}
	if chat.ProjectID != p.ID || chat.OwnerPersonID != requestscope.Caller(r).ID {
		s.responses.Fail(w, wire.ApiErrorCodeNotFound, "Command observation not found.")
		return
	}
	windowID := chi.URLParam(r, "window_id")
	windows, err := s.SourceLedger.Commands.CommandWindows(r.Context(), []string{windowID})
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	window, found := windows[windowID]
	if !found || window.ProjectID != p.ID || window.SessionID != chat.ID {
		s.responses.Fail(w, wire.ApiErrorCodeNotFound, "Command observation not found.")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.CommandObservationStatus{
		Settling: window.State == wire.SourceCommandWindowRunning,
		Coverage: commandObservationGaps(p, window),
	})
}

func commandObservationGaps(p *project.Project, window sourceledger.CommandWindow) []wire.CommandFolderCoverage {
	folders := make(map[string]string, len(p.Roots))
	for _, root := range p.Roots {
		folders[root.ID] = root.Path
	}
	out := []wire.CommandFolderCoverage{}
	for _, coverage := range window.Coverage {
		if coverage.State == wire.SourceWindowCoverageComplete {
			continue
		}
		folder := folders[coverage.RootID]
		if coverage.RootID == p.Outside.ID {
			folder = projectroot.OutsideAbs(coverage.Path)
		}
		if folder == "" {
			continue
		}
		out = append(out, coverage.ActivityFact(folder))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Folder < out[j].Folder })
	return out
}
